package service

import (
	fp "path/filepath"
	"sync"

	log "github.com/Financial-Times/go-logger"
)

type AggregateSuggester struct {
	Concordance     *ConcordanceService
	BroaderProvider *BroaderConceptsProvider
	Blacklister     ConceptBlacklister
	Suggesters      []Suggester
}

func NewAggregateSuggester(concordance *ConcordanceService, broaderConceptsProvider *BroaderConceptsProvider, blacklister ConceptBlacklister, suggesters ...Suggester) *AggregateSuggester {
	return &AggregateSuggester{
		Concordance:     concordance,
		Suggesters:      suggesters,
		BroaderProvider: broaderConceptsProvider,
		Blacklister:     blacklister,
	}
}

func (suggester *AggregateSuggester) GetSuggestions(payload []byte, tid string) (SuggestionsResponse, error) {
	data, err := getXmlSuggestionRequestFromJson(payload)
	// TODO:
	//	log.WithTransactionID(tid).WithField("debug", flags.Debug).Info(string(data))

	if err != nil {
		data = payload
	}
	var aggregateResp = SuggestionsResponse{Suggestions: make([]Suggestion, 0)}

	blacklistChannel := make(chan Blacklist, 1)
	go fetchBlacklist(suggester.Blacklister, blacklistChannel, tid)

	var mutex = sync.Mutex{}
	var wg = sync.WaitGroup{}

	var responseMap = map[int][]Suggestion{}
	for key, suggesterDelegate := range suggester.Suggesters {
		wg.Add(1)
		go func(i int, delegate Suggester) {
			resp, err := delegate.GetSuggestions(data, tid)
			if err != nil {
				if err == NoContentError || err == BadRequestError {
					log.WithTransactionID(tid).WithField("tid", tid).Warn(err.Error())
				} else {
					log.WithTransactionID(tid).WithField("tid", tid).WithError(err).Errorf("Error calling %v", delegate.GetName())
				}
			}
			mutex.Lock()
			responseMap[i] = resp.Suggestions
			mutex.Unlock()
			wg.Done()
		}(key, suggesterDelegate)
	}
	wg.Wait()

	responseMap, err = suggester.filterByInternalConcordances(responseMap, tid)
	if err != nil {
		return aggregateResp, err
	}

	for key, suggesterDelegate := range suggester.Suggesters {
		if len(responseMap[key]) > 0 {
			responseMap[key] = suggesterDelegate.FilterSuggestions(responseMap[key])
		}
	}

	results, err := suggester.BroaderProvider.excludeBroaderConceptsFromResponse(responseMap, tid)
	if err != nil {
		log.WithError(err).Warn("Couldn't exclude broader concepts. Response might contain broader concepts as well")
	} else {
		responseMap = results
	}

	blacklist := <-blacklistChannel

	// preserve results order
	for i := 0; i < len(suggester.Suggesters); i++ {
		for _, suggestion := range responseMap[i] {
			if suggester.Blacklister.IsBlacklisted(suggestion.ID, blacklist) {
				log.WithTransactionID(tid).Info("Suppressing suggestion for concept ", suggestion.ID)
			} else {
				aggregateResp.Suggestions = append(aggregateResp.Suggestions, suggestion)
			}
		}
	}
	return aggregateResp, nil
}

func fetchBlacklist(b ConceptBlacklister, c chan Blacklist, tid string) {
	blacklist, err := b.GetBlacklist(tid)
	if err != nil {
		log.WithTransactionID(tid).WithError(err).Errorf("Error retrieving concept blacklist, filtering disabled")
	}
	c <- blacklist
	close(c)
}

func (suggester *AggregateSuggester) filterByInternalConcordances(s map[int][]Suggestion, tid string) (map[int][]Suggestion, error) {
	// TODO:
	//	log.WithTransactionID(tid).WithField("debug", debugFlag).Info("Calling internal concordances")

	var filtered = map[int][]Suggestion{}
	var concorded ConcordanceResponse

	var ids []string
	for i := 0; i < len(s); i++ {
		for _, suggestion := range s[i] {
			ids = append(ids, fp.Base(suggestion.Concept.ID))
		}
	}

	ids = dedup(ids)

	if len(ids) == 0 {
		log.WithTransactionID(tid).Info("No suggestions for calling internal concordances!")
		return filtered, nil
	}

	concorded, err := suggester.Concordance.getConcordances(ids, tid)
	if err != nil {
		return filtered, err
	}

	total := 0
	for index, suggestions := range s {
		filtered[index] = []Suggestion{}
		for _, suggestion := range suggestions {
			id := fp.Base(suggestion.Concept.ID)
			c, ok := concorded.Concepts[id]
			if ok {
				filtered[index] = append(filtered[index], Suggestion{
					Predicate: suggestion.Predicate,
					Concept:   c,
				})
			}
		}
		total += len(filtered[index])
	}

	// TODO
	//	log.WithTransactionID(tid).WithField("debug", debugFlag).Infof("Retained %v of %v concepts using concordances", total, len(ids))

	return filtered, nil
}

func dedup(s []string) []string {
	seen := make(map[string]struct{}, len(s))
	j := 0
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		s[j] = v
		j++
	}
	return s[:j]
}
