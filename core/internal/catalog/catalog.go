// SPDX-License-Identifier: GPL-3.0-only

package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const maxCatalogBytes = 8 << 20

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func List(ctx context.Context, endpoint, match string) ([]Model, error) {
	client := http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ALLAIIS/0.1")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
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
	return parse(body, match)
}

func parse(body []byte, match string) ([]Model, error) {
	var catalog struct {
		Data []struct {
			Model
			Spec struct {
				Name string `json:"name"`
			} `json:"model_spec"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil || catalog.Data == nil {
		return nil, errors.New("provider returned an invalid model catalog")
	}

	models := make([]Model, 0, len(catalog.Data))
	seen := make(map[string]bool)
	match = strings.ToLower(strings.TrimSpace(match))
	for _, item := range catalog.Data {
		if item.ID == "" || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		name := item.Name
		if name == "" {
			name = item.Spec.Name
		}
		if name == "" {
			name = item.ID
		}
		if strings.Contains(strings.ToLower(item.ID), match) || strings.Contains(strings.ToLower(name), match) {
			models = append(models, Model{ID: item.ID, Name: name})
		}
	}
	slices.SortFunc(models, func(a, b Model) int { return strings.Compare(a.ID, b.ID) })
	return models, nil
}
