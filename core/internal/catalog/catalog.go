// SPDX-License-Identifier: GPL-3.0-only

package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

const (
	maxCatalogBytes = 8 << 20
	maxCatalogPages = 100
	maxCatalogRows  = 25000
)

type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Type    string   `json:"type,omitempty"`
	Methods []string `json:"supported_generation_methods,omitempty"`
	// Capabilities are filled when the source publishes them.
	Reasoning bool `json:"reasoning,omitempty"`
	Tools     bool `json:"function_calling,omitempty"`
	Vision    bool `json:"vision,omitempty"`
	MaxImages int  `json:"max_images,omitempty"`
	MaxTools  int  `json:"max_tools,omitempty"`
	WebSearch bool `json:"web_search,omitempty"`
	Context   int  `json:"max_context,omitempty"`
	Response  int  `json:"max_response,omitempty"`
	ProOnly   bool `json:"pro_only,omitempty"`
	Credits   bool `json:"uses_credits,omitempty"`
	// Sampling defaults the provider recommends for this model.
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
}

func List(ctx context.Context, endpoint string, format config.CatalogFormat, key, match string) ([]Model, error) {
	endpointURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := endpointURL.Query()
	pages := pagination{query: query}
	switch format {
	case config.DashScopeCatalog:
		query.Set("page_no", "1")
		query.Set("page_size", "100")
	case config.GeminiCatalog:
		query.Set("pageSize", "100")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()

	models := make([]Model, 0)
	seen := make(map[[2]string]struct{})
	parameters := make(map[string]struct{})
	match = strings.ToLower(strings.TrimSpace(match))
	for range maxCatalogPages {
		endpointURL.RawQuery = query.Encode()
		if _, duplicate := parameters[endpointURL.RawQuery]; duplicate {
			return nil, errors.New("provider repeated a catalog page cursor")
		}
		parameters[endpointURL.RawQuery] = struct{}{}
		body, err := readPage(ctx, &client, endpointURL.String(), format, key)
		if err != nil {
			return nil, err
		}
		rows, more, err := pages.decode(body, format)
		if err != nil {
			return nil, err
		}
		if pages.rows > maxCatalogRows {
			return nil, errors.New("provider catalog exceeds 25,000 entries")
		}
		for _, row := range rows {
			model := row.model(format)
			if model.ID == "" {
				continue
			}
			identity := [2]string{model.ID, model.Type}
			if _, duplicate := seen[identity]; duplicate {
				continue
			}
			seen[identity] = struct{}{}
			if strings.Contains(strings.ToLower(model.ID), match) || strings.Contains(strings.ToLower(model.Name), match) {
				models = append(models, model)
			}
		}
		if !more {
			slices.SortFunc(models, func(a, b Model) int {
				if byID := strings.Compare(a.ID, b.ID); byID != 0 {
					return byID
				}
				return strings.Compare(a.Type, b.Type)
			})
			return models, nil
		}
	}
	return nil, errors.New("provider catalog exceeds 100 pages")
}

func readPage(ctx context.Context, client *http.Client, endpoint string, format config.CatalogFormat, key string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ALLAIIS/0.1")
	if key != "" {
		switch format {
		case config.ClaudeCatalog:
			request.Header.Set("X-API-Key", key)
			request.Header.Set("Anthropic-Version", "2023-06-01")
		case config.GeminiCatalog:
			request.Header.Set("X-Goog-Api-Key", key)
		default:
			request.Header.Set("Authorization", "Bearer "+key)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not reach the provider catalog")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider catalog returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxCatalogBytes {
		return nil, errors.New("provider catalog exceeds 8 MiB")
	}
	return body, nil
}
