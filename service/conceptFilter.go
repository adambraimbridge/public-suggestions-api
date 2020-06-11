package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"sync"

	"github.com/Financial-Times/go-fthealth/v1_1"
)

type CachedConceptFilter struct {
	baseURL       string
	endpoint      string
	client        Client
	systemID      string
	name          string
	failureImpact string

	deniedUUIDs []string
	uuidsMx     *sync.RWMutex
	refreshing  bool
}

func NewCachedConceptFilter(baseURL string, endpoint string, client Client) *CachedConceptFilter {
	return &CachedConceptFilter{
		baseURL:       baseURL,
		endpoint:      endpoint,
		client:        client,
		systemID:      "concept-suggestions-blacklister",
		name:          "concept-suggestions-blacklister",
		failureImpact: "Suggestions vetoing will not work",
		uuidsMx:       &sync.RWMutex{},
		refreshing:    false,
	}
}

func (f *CachedConceptFilter) IsConceptAllowed(conceptID string) bool {
	f.uuidsMx.RLock()
	defer f.uuidsMx.RUnlock()
	for _, uuid := range f.deniedUUIDs {
		if strings.Contains(conceptID, uuid) {
			return false
		}
	}
	return true
}

func (f *CachedConceptFilter) RefreshCache(ctx context.Context, tid string) error {
	f.uuidsMx.Lock()
	defer f.uuidsMx.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.baseURL+f.endpoint, nil)
	if err != nil {
		return err
	}

	req.Header.Add("User-Agent", "UPP public-suggestions-api")
	req.Header.Add("Accept", "application/json")
	req.Header.Add("X-Request-Id", tid)

	q := req.URL.Query()
	q.Add("refresh", "true")
	req.URL.RawQuery = q.Encode()

	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("concept-suggestions-blacklister returned HTTP %v", resp.StatusCode)
	}

	var blacklist struct {
		UUIDS []string `json:"uuids"`
	}
	err = json.Unmarshal(body, &blacklist)
	if err != nil {
		return err
	}

	f.deniedUUIDs = blacklist.UUIDS
	return nil
}

func (f *CachedConceptFilter) Check() v1_1.Check {
	return v1_1.Check{
		ID:               f.systemID,
		BusinessImpact:   f.failureImpact,
		Name:             fmt.Sprintf("%v Healthcheck", f.name),
		PanicGuide:       PanicGuideURL + f.systemID,
		Severity:         2,
		TechnicalSummary: fmt.Sprintf("%v is not available", f.name),
		Checker:          f.healthCheck,
	}
}

func (f *CachedConceptFilter) healthCheck() (string, error) {
	req, err := http.NewRequest("GET", f.baseURL+"/__gtg", nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("User-Agent", "UPP public-suggestions-api")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("health check returned a non-200 HTTP status: %d", resp.StatusCode)
	}
	return fmt.Sprintf("%s is healthy", f.name), nil
}
