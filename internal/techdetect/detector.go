// Package techdetect provides technology detection for websites using wappalyzergo.
// It identifies CMS platforms, CDNs, frameworks, and other technologies used by domains.
package techdetect

import (
	"encoding/json"
	"maps"
	"net/http"
	"sync"

	"github.com/good-native/hover/internal/logging"
	wappalyzer "github.com/projectdiscovery/wappalyzergo"
)

var techdetectLog = logging.Component("techdetect")

const MaxHTMLSampleSize = 50 * 1024

type Result struct {
	Technologies map[string][]string `json:"technologies"`
	RawHeaders   map[string][]string `json:"raw_headers"`
	HTMLSample   string              `json:"html_sample"` // truncated to MaxHTMLSampleSize
}

type Detector struct {
	client *wappalyzer.Wappalyze
	mu     sync.RWMutex
}

var categoryNames map[int]string
var categoryNamesOnce sync.Once

func New() (*Detector, error) {
	client, err := wappalyzer.New()
	if err != nil {
		return nil, err
	}

	categoryNamesOnce.Do(func() {
		categoryNames = make(map[int]string)
		cats := wappalyzer.GetCategoriesMapping()
		for id, cat := range cats {
			categoryNames[id] = cat.Name
		}
	})

	return &Detector{
		client: client,
	}, nil
}

func (d *Detector) Detect(headers http.Header, body []byte) *Result {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := &Result{
		Technologies: make(map[string][]string),
		RawHeaders:   make(map[string][]string),
	}

	maps.Copy(result.RawHeaders, headers)

	if len(body) > MaxHTMLSampleSize {
		result.HTMLSample = string(body[:MaxHTMLSampleSize])
	} else {
		result.HTMLSample = string(body)
	}

	fingerprints := d.client.FingerprintWithCats(headers, body)

	for tech, catInfo := range fingerprints {
		categories := make([]string, 0, len(catInfo.Cats))
		for _, catID := range catInfo.Cats {
			if name, ok := categoryNames[catID]; ok {
				categories = append(categories, name)
			}
		}
		result.Technologies[tech] = categories
	}

	techdetectLog.Debug("Technology detection completed",
		"tech_count", len(result.Technologies),
		"technologies", result.Technologies,
	)

	return result
}

func (d *Detector) DetectFromResponse(resp *http.Response, body []byte) *Result {
	return d.Detect(resp.Header, body)
}

func (r *Result) TechnologiesJSON() ([]byte, error) {
	return json.Marshal(r.Technologies)
}

func (r *Result) HeadersJSON() ([]byte, error) {
	return json.Marshal(r.RawHeaders)
}
