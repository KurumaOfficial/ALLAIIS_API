// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
)

// CatalogModel is one entry of the web app's model catalog.
type CatalogModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	UsesCredits  bool   `json:"uses_credits"`
	ProOnly      bool   `json:"pro_only,omitempty"`
	Privacy      string `json:"privacy,omitempty"`
	ContextLimit int    `json:"context,omitempty"`
	MaxResponse  int    `json:"max_response,omitempty"`
	MaxImages    int    `json:"max_images,omitempty"`
	MaxTools     int    `json:"max_tools,omitempty"`
	Reasoning    bool   `json:"reasoning,omitempty"`
	WebSearch    bool   `json:"web_search,omitempty"`
	LargeContext bool   `json:"large_context,omitempty"`
	StrictOrder  bool   `json:"strict_order,omitempty"`
	InactivityMS int    `json:"inactivity_ms,omitempty"`
	Sampling     bool   `json:"sampling,omitempty"`
	Frequency    bool   `json:"frequency_penalty,omitempty"`
	Presence     bool   `json:"presence_penalty,omitempty"`
	Replacement  string `json:"replacement,omitempty"`
	Retiring     bool   `json:"retiring,omitempty"`
	RoundTrip    bool   `json:"reasoning_round_trip,omitempty"`
	Video        bool   `json:"video,omitempty"`
	MaxVideos    int    `json:"max_videos,omitempty"`
	Tools        bool   `json:"function_calling,omitempty"`
	Temperature  *float64
	TopP         *float64
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
				ID           string `json:"id"`
				Friendly     string `json:"friendly_name"`
				Active       bool   `json:"active"`
				UsesCredits  bool   `json:"usesCredits"`
				ProOnly      bool   `json:"proOnly"`
				MaxContext   int    `json:"maxContextLength"`
				MaxResponse  int    `json:"maxResponseTokens"`
				MaxImages    int    `json:"maxImages"`
				MaxTools     int    `json:"maxToolCount"`
				WebSearch    bool   `json:"supportsXSearch"`
				LargeContext bool   `json:"largeContextChatEligible"`
				StrictOrder  bool   `json:"strictMessageOrderRequired"`
				InactivityMS int    `json:"streamInactivityTimeoutMs"`
				Sampling     bool   `json:"supportsSamplingParams"`
				Frequency    bool   `json:"supportsFrequencyPenalty"`
				Presence     bool   `json:"supportsPresencePenalty"`
				Replacement  string `json:"apiDeprecationReplacementModelId"`
				RetiringDate int64  `json:"retiringSoonDate"`
				RoundTrip    bool   `json:"supportsEncryptedReasoningRoundTrip"`
				Forward      bool   `json:"forwardEncryptedReasoning"`
				Video        bool   `json:"supportsVideoInput"`
				MaxVideos    int    `json:"maxVideos"`
				Reasoning    bool   `json:"supportsReasoning"`
				Tools        bool   `json:"supportsFunctionCalling"`
				Settings     struct {
					Temperature *float64 `json:"temperature"`
					TopP        *float64 `json:"topP"`
				} `json:"settings"`
			} `json:"models"`
		} `json:"text"`
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if os.Getenv("ALLAIIS_WEB_DEBUG") != "" {
		os.WriteFile(os.TempDir()+"/venice-catalog.json", raw, 0o600)
	}
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
		models = append(models, CatalogModel{
			ID: model.ID, Name: name, Kind: "text", UsesCredits: model.UsesCredits,
			ProOnly: model.ProOnly, ContextLimit: model.MaxContext, MaxResponse: model.MaxResponse,
			MaxImages: model.MaxImages, MaxTools: model.MaxTools, WebSearch: model.WebSearch,
			LargeContext: model.LargeContext, StrictOrder: model.StrictOrder, InactivityMS: model.InactivityMS,
			Sampling: model.Sampling, Frequency: model.Frequency, Presence: model.Presence,
			Replacement: model.Replacement, Retiring: model.RetiringDate > 0,
			RoundTrip: model.RoundTrip || model.Forward,
			Video:     model.Video, MaxVideos: model.MaxVideos,
			Reasoning: model.Reasoning, Tools: model.Tools,
			Temperature: model.Settings.Temperature, TopP: model.Settings.TopP,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
