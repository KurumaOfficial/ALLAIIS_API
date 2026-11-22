// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
)

// CatalogModel is one entry of the web app's model catalog.
type CatalogModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	UsesCredits  bool   `json:"uses_credits"`
	Privacy      string `json:"privacy,omitempty"`
	ContextLimit int    `json:"context,omitempty"`
}

// Catalog lists the text models the web app offers for this session.
func (c *Client) Catalog(ctx context.Context, cred Credential) ([]CatalogModel, error) {
	userID, err := c.user(ctx, cred)
	if err != nil {
		return nil, err
	}
	attestation, err := c.attestation(ctx, cred, userID)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/app/models?matureFilter=true&onlySafeVenice=true", nil)
	if err != nil {
		return nil, err
	}
	c.headers(request, cred, attestation)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, c.statusError(response)
	}
	var catalog struct {
		Text struct {
			Models []struct {
				ID          string `json:"id"`
				Friendly    string `json:"friendly_name"`
				Active      bool   `json:"active"`
				UsesCredits bool   `json:"usesCredits"`
			} `json:"models"`
		} `json:"text"`
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("could not read the model catalog: %v (%s)", err, string(raw[:min(len(raw), 200)]))
	}
	models := make([]CatalogModel, 0, len(catalog.Text.Models))
	for _, model := range catalog.Text.Models {
		if model.ID == "" || !model.Active {
			continue
		}
		name := model.Friendly
		if name == "" {
			name = model.ID
		}
		models = append(models, CatalogModel{ID: model.ID, Name: name, Kind: "text", UsesCredits: model.UsesCredits})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
