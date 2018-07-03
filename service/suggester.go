package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"

	health "github.com/Financial-Times/go-fthealth/v1_1"
	log "github.com/Financial-Times/go-logger"
)

const (
	personType = "http://www.ft.com/ontology/person/Person"
	hasAuthor  = "http://www.ft.com/ontology/annotation/hasAuthor"
)

var NoContentError = errors.New("Suggestion API returned HTTP 204")

type Client interface {
	Do(req *http.Request) (resp *http.Response, err error)
}

type Suggester interface {
	GetSuggestions(payload []byte, tid string) (SuggestionsResponse, error)
}

type AggregateSuggester struct {
	FalconSuggester  Suggester
	AuthorsSuggester Suggester
}

type SuggestionApi struct {
	name               string
	apiBaseURL         string
	suggestionEndpoint string
	client             Client
	systemId           string
	failureImpact      string
}

type SuggestionsResponse struct {
	Suggestions []Suggestion `json:"suggestions"`
}

type Suggestion struct {
	Predicate      string `json:"predicate,omitempty"`
	Id             string `json:"id,omitempty"`
	ApiUrl         string `json:"apiUrl,omitempty"`
	PrefLabel      string `json:"prefLabel,omitempty"`
	SuggestionType string `json:"type,omitempty"`
	IsFTAuthor     bool   `json:"isFTAuthor,omitempty"`
}

func NewFalconSuggester(falconSuggestionApiBaseURL, falconSuggestionEndpoint string, client Client) *SuggestionApi {
	return &SuggestionApi{
		apiBaseURL:         falconSuggestionApiBaseURL,
		suggestionEndpoint: falconSuggestionEndpoint,
		client:             client,
		name:               "Falcon Suggestion API",
		systemId:           "falcon-suggestion-api",
		failureImpact:      "Suggestions from TME won't work",
	}
}

func NewAuthorsSuggester(authorsSuggestionApiBaseURL, authorsSuggestionEndpoint string, client Client) *SuggestionApi {
	return &SuggestionApi{
		apiBaseURL:         authorsSuggestionApiBaseURL,
		suggestionEndpoint: authorsSuggestionEndpoint,
		client:             client,
		name:               "Authors Suggestion API",
		systemId:           "authors-suggestion-api",
		failureImpact:      "Suggesting authors from Concept Search won't work",
	}
}

func NewAggregateSuggester(falconSuggester, authorsSuggester Suggester) Suggester {
	return &AggregateSuggester{FalconSuggester: falconSuggester, AuthorsSuggester: authorsSuggester}
}

func (suggester *AggregateSuggester) GetSuggestions(payload []byte, tid string) (SuggestionsResponse, error) {
	fResp, err := suggester.FalconSuggester.GetSuggestions(payload, tid)
	if err != nil {
		if err == NoContentError {
			log.WithTransactionID(tid).WithField("tid", tid).Warn(err.Error())
		} else {
			log.WithTransactionID(tid).WithField("tid", tid).WithError(err).Error("Error calling Falcon Suggestions API")
		}
	}
	aResp, err := suggester.AuthorsSuggester.GetSuggestions(payload, tid)
	if err != nil {
		if err == NoContentError {
			log.WithTransactionID(tid).WithField("tid", tid).Warn(err.Error())
		} else {
			log.WithTransactionID(tid).WithField("tid", tid).WithError(err).Error("Error calling Authors Suggestions API")
		}
	}
	//filter out authors response from falcon response if there is an authors response
	if len(aResp.Suggestions) > 0 {
		i := 0
		for _, value := range fResp.Suggestions {
			if isNotAuthor(value) {
				//retain suggestion from falcon response
				fResp.Suggestions[i] = value
				i++
			}
		}
		fResp.Suggestions = fResp.Suggestions[:i]
	}
	// merge results
	// return empty slice by default instead of nil/null suggestions response
	var resp = SuggestionsResponse{
		Suggestions:make([]Suggestion, 0, len(aResp.Suggestions) + len(fResp.Suggestions)),
	}

	resp.Suggestions = append(resp.Suggestions, aResp.Suggestions...)
	resp.Suggestions = append(resp.Suggestions, fResp.Suggestions...)
	//no error should be returned, so clients could get always status OK
	return resp, nil
}

func isNotAuthor(value Suggestion) bool {
	return !(value.SuggestionType == personType && value.Predicate == hasAuthor)
}

func (suggester *SuggestionApi) GetSuggestions(payload []byte, tid string) (SuggestionsResponse, error) {
	req, err := http.NewRequest("POST", suggester.apiBaseURL+suggester.suggestionEndpoint, bytes.NewReader(payload))
	if err != nil {
		return SuggestionsResponse{}, err
	}

	req.Header.Add("User-Agent", "UPP public-suggestions-api")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("X-Request-Id", tid)

	resp, err := suggester.client.Do(req)
	if err != nil {
		return SuggestionsResponse{}, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return SuggestionsResponse{}, err
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNoContent {
			return SuggestionsResponse{make([]Suggestion, 0)}, NoContentError
		}
		return SuggestionsResponse{}, fmt.Errorf("%v returned HTTP %v", suggester.name, resp.StatusCode)
	}

	var response SuggestionsResponse
	err = json.Unmarshal(body, &response)
	if err != nil {
		return SuggestionsResponse{}, err
	}
	return response, nil
}

func (suggester *SuggestionApi) Check() health.Check {
	return health.Check{
		ID:               suggester.systemId,
		BusinessImpact:   suggester.failureImpact,
		Name:             fmt.Sprintf("%v Healthcheck", suggester.name),
		PanicGuide:       "https://dewey.in.ft.com/view/system/public-suggestions-api",
		Severity:         2,
		TechnicalSummary: fmt.Sprintf("%v is not available", suggester.name),
		Checker:          suggester.healthCheck,
	}
}

func (suggester *SuggestionApi) healthCheck() (string, error) {
	req, err := http.NewRequest("GET", suggester.apiBaseURL+"/__gtg", nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("User-Agent", "UPP public-suggestions-api")

	resp, err := suggester.client.Do(req)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Health check returned a non-200 HTTP status: %v", resp.StatusCode)
	}
	return fmt.Sprintf("%v is healthy", suggester.name), nil
}