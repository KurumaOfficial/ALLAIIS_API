// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/catalog"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

type providerView struct {
	ID            string               `json:"id"`
	BaseURL       string               `json:"base_url"`
	Web           bool                 `json:"web"`
	CatalogPublic bool                 `json:"catalog_public"`
	Labels        []string             `json:"labels"`
	Cooldowns     map[string]time.Time `json:"cooldowns,omitempty"`
	CooldownUntil *time.Time           `json:"cooldown_until,omitempty"`
}

type modelView struct {
	ID            string          `json:"id"`
	Provider      string          `json:"provider"`
	UpstreamModel string          `json:"upstream_model"`
	Protocol      config.Protocol `json:"protocol"`
	Privacy       config.Privacy  `json:"privacy"`
	Label         string          `json:"label"`
	Available     bool            `json:"available"`
	// BalanceUntil marks this model parked for an empty balance on its label.
	BalanceUntil *time.Time `json:"balance_until,omitempty"`
	Disabled     bool       `json:"disabled"`
	Reasoning    *bool      `json:"reasoning,omitempty"`
	MaxContext   int        `json:"max_context,omitempty"`
	MaxResponse  int        `json:"max_response,omitempty"`
	MaxImages    int        `json:"max_images,omitempty"`
	MaxTools     int        `json:"max_tools,omitempty"`
	WebSearch    bool       `json:"web_search,omitempty"`
	LargeContext bool       `json:"large_context,omitempty"`
	StrictOrder  bool       `json:"strict_order,omitempty"`
	InactivityMS int        `json:"inactivity_ms,omitempty"`
	NoSampling   bool       `json:"no_sampling,omitempty"`
	Frequency    bool       `json:"frequency_penalty,omitempty"`
	Presence     bool       `json:"presence_penalty,omitempty"`
	Replacement  string     `json:"replacement,omitempty"`
	RoundTrip    bool       `json:"reasoning_round_trip,omitempty"`
	E2EE         bool       `json:"e2ee,omitempty"`
	MaxVideos    int        `json:"max_videos,omitempty"`
	MinP         bool       `json:"min_p,omitempty"`
	AnonUserID   *bool      `json:"anon_user_id,omitempty"`
	RateLimit    string     `json:"rate_limit,omitempty"`
}

type stateView struct {
	Version     string         `json:"version"`
	Listen      string         `json:"listen"`
	DataDir     string         `json:"data_dir"`
	ConfigPath  string         `json:"config_path"`
	MinPrivacy  config.Privacy `json:"min_privacy"`
	ClientKey   string         `json:"client_key"`
	Reachable   bool           `json:"reachable"`
	Providers   []providerView `json:"providers"`
	Models      []modelView    `json:"models"`
	Usage       map[string]any `json:"usage"`
	PrivacySeen []string       `json:"privacy_levels"`
	SmokeEvery  string         `json:"smoke_every,omitempty"`
}

func (s *Server) snapshot(ctx context.Context) (stateView, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return stateView{}, err
	}
	view := stateView{
		Version: s.options.Version, Listen: cfg.Listen, DataDir: s.options.DataDir,
		ConfigPath: s.options.ConfigPath, MinPrivacy: cfg.MinPrivacy,
		ClientKey: s.options.Store.ClientKey(),
		Usage:     s.options.Gateway.Metrics(),
		PrivacySeen: []string{
			string(config.Public), string(config.Consumer), string(config.ZDR), string(config.E2EE),
		},
	}
	view.Reachable = reachable(cfg.Listen)
	if s.options.SmokeEvery > 0 {
		view.SmokeEvery = s.options.SmokeEvery.String()
	}

	ids := cfg.ProviderIDs()
	for id := range cfg.Providers {
		if !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	balances := map[string]time.Time{}
	for _, id := range ids {
		provider, ok := cfg.Provider(id)
		if !ok {
			continue
		}
		entries, err := s.options.Store.Entries(ctx, id, provider.BaseURL)
		if err != nil {
			return stateView{}, err
		}
		record := providerView{
			ID: id, BaseURL: provider.BaseURL, Web: provider.Web(),
			CatalogPublic: provider.CatalogPublic, Labels: make([]string, 0, len(entries)),
		}
		rows, _ := s.options.Store.Cooldowns(ctx, id)
		for _, entry := range entries {
			record.Labels = append(record.Labels, entry.Label)
			if until, ok := rows[provider.BaseURL+"\x00"+entry.Label]; ok && time.Now().Before(until) {
				if record.Cooldowns == nil {
					record.Cooldowns = map[string]time.Time{}
				}
				record.Cooldowns[entry.Label] = until
			}
		}
		if until, ok := rows[provider.BaseURL]; ok && time.Now().Before(until) {
			record.CooldownUntil = &until
		}
		for key, until := range rows {
			if rest, found := strings.CutPrefix(key, provider.BaseURL+"\x00"); found {
				parts := strings.Split(rest, "\x00")
				if len(parts) == 2 && time.Now().Before(until) {
					balances[parts[1]] = until
				}
			}
		}
		view.Providers = append(view.Providers, record)
	}
	for _, route := range cfg.Models {
		var balance *time.Time
		if until, ok := balances[route.ID]; ok {
			value := until
			balance = &value
		}
		record := modelView{
			ID: route.ID, Provider: route.Provider, UpstreamModel: route.UpstreamModel,
			Protocol: route.Protocol, Privacy: route.Privacy, Label: route.CredentialLabel,
			Disabled: route.Disabled, Reasoning: route.Reasoning, MaxContext: route.MaxContext,
			MaxResponse: route.MaxResponse, MaxImages: route.MaxImages,
			MaxTools: route.MaxTools, WebSearch: route.WebSearch,
			LargeContext: route.LargeContext, StrictOrder: route.StrictOrder, InactivityMS: route.InactivityMS,
			NoSampling: route.NoSampling, Frequency: route.Frequency, Presence: route.Presence,
			Replacement: route.Replacement, RoundTrip: route.ReasoningRoundTrip, MaxVideos: route.MaxVideos, E2EE: route.E2EE,
			MinP: route.MinP, AnonUserID: route.AnonUserID, BalanceUntil: balance,
		}
		for _, provider := range view.Providers {
			if provider.ID != route.Provider {
				continue
			}
			if route.CredentialLabel == "" {
				record.Available = len(provider.Labels) > 0
			} else {
				record.Available = contains(provider.Labels, route.CredentialLabel)
			}
			break
		}
		view.Models = append(view.Models, record)
	}
	sort.Slice(view.Models, func(i, j int) bool { return view.Models[i].ID < view.Models[j].ID })
	return view, nil
}

func reachable(address string) bool {
	connection, err := net.DialTimeout("tcp", address, 300*time.Millisecond)
	if err != nil {
		return false
	}
	connection.Close()
	return true
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.URL.Query().Get("provider")
	provider, ok := cfg.Provider(id)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown provider")
		return
	}
	label := r.URL.Query().Get("label")
	match := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("match")))
	cacheKey := id + "\x00" + label + "\x00" + match
	if r.URL.Query().Get("refresh") == "" {
		if models, ok := s.catalogs.get(cacheKey); ok {
			writeJSON(w, http.StatusOK, map[string]any{"models": models, "cached": true})
			return
		}
	}
	entries, err := s.options.Store.Entries(r.Context(), id, provider.BaseURL)
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	var entry vault.Entry
	switch {
	case label != "":
		for _, candidate := range entries {
			if candidate.Label == label {
				entry = candidate
			}
		}
		if entry.Label == "" {
			fail(w, http.StatusBadRequest, "that credential label is not stored")
			return
		}
	case len(entries) == 1:
		entry = entries[0]
	case len(entries) > 1 && !provider.CatalogPublic:
		fail(w, http.StatusBadRequest, "pick a credential label first")
	}
	if provider.Web() {
		credential, err := webprov.ParseCredential(entry.Key)
		if err != nil {
			fail(w, http.StatusBadRequest, "the stored web session is unreadable")
			return
		}
		models, err := s.webClient(r.Context(), 60*time.Second).Catalog(r.Context(), credential)
		if err != nil {
			fail(w, http.StatusBadGateway, err.Error())
			return
		}
		result := make([]catalog.Model, 0, len(models))
		for _, model := range models {
			if match != "" && !strings.Contains(strings.ToLower(model.ID), match) &&
				!strings.Contains(strings.ToLower(model.Name), match) {
				continue
			}
			result = append(result, catalog.Model{
				ID: model.ID, Name: model.Name, Type: model.Kind,
				Reasoning: model.Reasoning, Tools: model.Tools, Vision: model.MaxImages > 0,
				MaxImages: model.MaxImages, Context: model.ContextLimit, Response: model.MaxResponse,
				ProOnly: model.ProOnly, Credits: model.UsesCredits,
				MaxTools: model.MaxTools, WebSearch: model.WebSearch,
				LargeContext: model.LargeContext, StrictOrder: model.StrictOrder, Inactivity: model.InactivityMS,
				Sampling: model.Sampling, Frequency: model.Frequency, Presence: model.Presence,
				Replacement: model.Replacement, Retiring: model.Retiring, RoundTrip: model.RoundTrip, E2EE: model.E2EE,
				Video: model.Video, MaxVideos: model.MaxVideos,
				MinP: model.MinP, AnonUserID: model.AnonUserID, RateLimit: model.RateLimit,
				Temperature: model.Temperature, TopP: model.TopP,
			})
			if len(result) >= 500 {
				break
			}
		}
		s.catalogs.put(cacheKey, result)
		writeJSON(w, http.StatusOK, map[string]any{"models": result})
		return
	}
	if entry.Key == "" && !provider.CatalogPublic {
		fail(w, http.StatusBadRequest, "this provider needs a stored key")
		return
	}
	models, err := catalog.List(r.Context(), provider.CatalogURL(), provider.CatalogFormat, entry.Key, match)
	if err != nil {
		fail(w, http.StatusBadGateway, err.Error())
		return
	}
	s.catalogs.put(cacheKey, models)
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

type probeResult struct {
	Alias    string `json:"alias,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Status   int    `json:"status"`
	OK       bool   `json:"ok"`
	Millis   int64  `json:"millis"`
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
	Upstream string `json:"upstream_model,omitempty"`
	Emulated string `json:"emulated,omitempty"`
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Model  string `json:"model"`
		Prompt string `json:"prompt"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	route, ok := findRoute(cfg, input.Model)
	if !ok {
		fail(w, http.StatusNotFound, "unknown alias")
		return
	}
	result := s.probeOne(r.Context(), cfg, route, input.Prompt)
	writeJSON(w, http.StatusOK, result)
}

// probeTools runs the parser check on one alias on demand.
func (s *Server) probeTools(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &input) {
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
	}
	route, ok := findRoute(cfg, input.Model)
	if !ok {
		fail(w, http.StatusBadRequest, "unknown alias")
		return
	}
	if route.Disabled {
		fail(w, http.StatusBadRequest, "that alias is muted")
		return
	}
	writeJSON(w, http.StatusOK, s.toolProbe(r.Context(), cfg, route))
}

// smoke runs every alias that has a credential, sequentially.
func (s *Server) smoke(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Alias string `json:"alias"`
	}
	decode(w, r, &input)
	results, passed, failed := s.smokeRun(r.Context(), input.Alias)
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "passed": passed, "failed": failed})
}

// smokeRun probes the aliases with a credential in sorted order.
func (s *Server) smokeRun(ctx context.Context, alias string) ([]probeResult, int, int) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, 0, 0
	}
	labels := map[string][]string{}
	for _, route := range cfg.Models {
		if _, known := labels[route.Provider]; known {
			continue
		}
		provider, ok := cfg.Provider(route.Provider)
		if !ok {
			continue
		}
		entries, err := s.options.Store.Entries(ctx, route.Provider, provider.BaseURL)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			labels[route.Provider] = append(labels[route.Provider], entry.Label)
		}
	}
	models := append([]config.Route{}, cfg.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	results := make([]probeResult, 0, len(models)+1)
	passed, failed := 0, 0
	var toolRoutes []config.Route
	for _, route := range models {
		if route.Disabled || (alias != "" && route.ID != alias) {
			continue
		}
		names := labels[route.Provider]
		if (route.CredentialLabel == "" && len(names) == 0) || (route.CredentialLabel != "" && !contains(names, route.CredentialLabel)) {
			continue
		}
		provider, _ := cfg.Provider(route.Provider)
		result := probeResult{Status: http.StatusTooManyRequests}
		key := provider.BaseURL
		if route.CredentialLabel != "" {
			key += "\x00" + route.CredentialLabel
		}
		until, err := s.options.Store.Cooldown(ctx, route.Provider, key)
		if bare, bareErr := s.options.Store.Cooldown(ctx, route.Provider, provider.BaseURL); bareErr == nil && bare.After(until) {
			until = bare
		}
		// A model-scoped balance mark means the call can only answer 402; the
		// sweep used to spend the request anyway and report a failure.
		var balance time.Time
		if route.CredentialLabel != "" {
			balance, _ = s.options.Store.Cooldown(ctx, route.Provider, key+"\x00"+route.ID)
		}
		switch {
		case !balance.IsZero() && time.Now().Before(balance):
			result.Status = http.StatusPaymentRequired
			result.Error = "model has no balance until " + balance.Format("15:04:05")
		case err == nil && time.Now().Before(until):
			result.Error = "provider cooling until " + until.Format("15:04:05")
		default:
			result = s.probeOne(ctx, cfg, route, "Reply with the single word: ready")
		}
		result.Alias = route.ID
		result.Protocol = string(route.Protocol)
		if result.Protocol == "" {
			result.Protocol = "chat"
		}
		result.OK = result.Status == http.StatusOK && result.Text != ""
		if result.OK {
			passed++
			if len(toolRoutes) < 2 {
				toolRoutes = append(toolRoutes, route)
			}
		} else {
			failed++
		}
		results = append(results, result)
	}
	// One alias carries the tool probe, so a parser regression surfaces in the
	// scheduled sweep without paying for a tool call on every alias. A second
	// alias stands in when the first cannot produce a call at all, so a model
	// without tool support does not keep the row red on its own.
	if len(toolRoutes) > 0 && !s.options.NoToolProbe {
		tool := s.toolProbe(ctx, cfg, toolRoutes[0])
		if !tool.OK && len(toolRoutes) > 1 {
			tool = s.toolProbe(ctx, cfg, toolRoutes[1])
		}
		results = append(results, tool)
		if tool.OK {
			passed++
		} else {
			failed++
		}
	}
	return results, passed, failed
}

// toolProbe asks a healthy alias to call a tool and reports whether a call with
// the exact name came back. The name collides with no catalog tool, so the guard
// must deliver it rather than drop it; a mangled name is a finding of its own.
//
// Prose happens on weak models, so one firmer retry runs before a failure: the
// second prompt spells the answer out. Only a second prose costs the extra call.
func (s *Server) toolProbe(ctx context.Context, cfg config.Config, route config.Route) probeResult {
	prompts := []string{
		"Call the tool list_files once with path . and nothing else. Do not answer in prose.",
		"Use the list_files tool. Arguments: {\"path\": \".\"}. Reply with the tool call only, no prose.",
	}
	var result probeResult
	for attempt, prompt := range prompts {
		result = s.toolAttempt(ctx, cfg, route, prompt)
		if result.OK {
			break
		}
		if attempt == len(prompts)-1 || result.Status != http.StatusOK {
			break
		}
	}
	return result
}

// toolAttempt sends one tool probe and reads the reply.
func (s *Server) toolAttempt(ctx context.Context, cfg config.Config, route config.Route, prompt string) probeResult {
	result := probeResult{Alias: route.ID + " (tools)", Protocol: string(route.Protocol)}
	if result.Protocol == "" {
		result.Protocol = "chat"
	}
	payload, err := json.Marshal(map[string]any{
		"model": route.ID, "max_tokens": 128, "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name":        "list_files",
			"description": "List the files in a directory",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			},
		}}},
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+cfg.Listen+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	request.Header.Set("Authorization", "Bearer "+s.options.Store.ClientKey())
	request.Header.Set("Content-Type", "application/json")
	// The gateway keeps probe names out of the unknown counters and the record.
	request.Header.Set("X-ALLAIIS-Probe", "smoke")
	start := time.Now()
	response, err := (&http.Client{Timeout: 150 * time.Second}).Do(request)
	result.Millis = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	result.Status = response.StatusCode
	if response.StatusCode != http.StatusOK {
		result.Error = failureText(raw)
		return result
	}
	result.Text, result.OK = toolCallText(raw, route.Protocol)
	if !result.OK {
		result.Error = "answered without a tool call"
		return result
	}
	// A call under another name means the guard or the model mangled it; a call
	// without the required field means the repair did not fill it.
	if !strings.HasPrefix(result.Text, "list_files ") {
		result.OK = false
		result.Error = "called " + strings.Fields(result.Text)[0] + " instead of list_files"
		return result
	}
	if !strings.Contains(result.Text, `"path"`) {
		result.OK = false
		result.Error = "called list_files without path"
	}
	return result
}

// toolCallText reports whether the reply holds a tool call and what it is.
func toolCallText(raw []byte, protocol config.Protocol) (string, bool) {
	switch protocol {
	case config.Messages:
		var reply struct {
			Content []struct {
				Type  string          `json:"type"`
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &reply) != nil {
			return "", false
		}
		for _, block := range reply.Content {
			if block.Type == "tool_use" {
				return block.Name + " " + string(block.Input), true
			}
		}
		return "", false
	case config.Responses:
		var reply struct {
			Output []struct {
				Type      string `json:"type"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"output"`
		}
		if json.Unmarshal(raw, &reply) != nil {
			return "", false
		}
		for _, item := range reply.Output {
			if item.Type == "function_call" {
				return item.Name + " " + item.Arguments, true
			}
		}
		return "", false
	default:
		var reply struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(raw, &reply) != nil || len(reply.Choices) == 0 {
			return "", false
		}
		calls := reply.Choices[0].Message.ToolCalls
		if len(calls) == 0 {
			return "", false
		}
		return calls[0].Function.Name + " " + calls[0].Function.Arguments, true
	}
}

func findRoute(cfg config.Config, alias string) (config.Route, bool) {
	for _, candidate := range cfg.Models {
		if candidate.ID == alias {
			return candidate, true
		}
	}
	return config.Route{}, false
}

func (s *Server) probeOne(ctx context.Context, cfg config.Config, route config.Route, prompt string) probeResult {
	var result probeResult
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		prompt = "Reply with the single word: ready"
	}
	var path string
	var body any
	headers := map[string]string{"Authorization": "Bearer " + s.options.Store.ClientKey()}
	switch route.Protocol {
	case config.Messages:
		path = "/v1/messages"
		body = map[string]any{
			"model": route.ID, "max_tokens": 64, "stream": false,
			"messages": []any{map[string]any{"role": "user", "content": prompt}},
		}
		headers["Anthropic-Version"] = "2023-06-01"
	case config.Responses:
		path = "/v1/responses"
		body = map[string]any{"model": route.ID, "input": prompt, "max_output_tokens": 64}
	default:
		path = "/v1/chat/completions"
		body = map[string]any{
			"model": route.ID, "max_tokens": 64, "stream": false,
			"messages": []any{map[string]any{"role": "user", "content": prompt}},
		}
	}
	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+cfg.Listen+path, bytes.NewReader(payload))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", "application/json")
	start := time.Now()
	response, err := (&http.Client{Timeout: 150 * time.Second}).Do(request)
	result.Millis = time.Since(start).Milliseconds()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	result.Status = response.StatusCode
	result.Upstream = response.Header.Get("X-ALLAIIS-Upstream-Model")
	result.Emulated = response.Header.Get("X-ALLAIIS-Emulated-Features")
	if response.StatusCode == http.StatusOK {
		result.Text = answerText(raw, route.Protocol)
	} else {
		result.Error = failureText(raw)
	}
	return result
}

func answerText(raw []byte, protocol config.Protocol) string {
	switch protocol {
	case config.Messages:
		var reply struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		json.Unmarshal(raw, &reply)
		var built strings.Builder
		for _, block := range reply.Content {
			if block.Text != "" {
				built.WriteString(block.Text)
			}
			if block.Type == "tool_use" {
				built.WriteString("\n[tool_use]")
			}
		}
		return built.String()
	case config.Responses:
		var reply struct {
			Output []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		json.Unmarshal(raw, &reply)
		var built strings.Builder
		for _, item := range reply.Output {
			for _, part := range item.Content {
				built.WriteString(part.Text)
			}
		}
		return built.String()
	default:
		var reply struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		json.Unmarshal(raw, &reply)
		if len(reply.Choices) > 0 {
			return reply.Choices[0].Message.Content
		}
	}
	return strings.TrimSpace(string(raw))
}

func failureText(raw []byte) string {
	var reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &reply) == nil && reply.Error.Message != "" {
		return reply.Error.Message
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 400 {
		text = text[:400]
	}
	if text == "" {
		return "empty response"
	}
	return text
}

// aliasFor builds a readable, valid alias from a provider and model name.
func aliasFor(provider, model string) string { return config.DefaultAlias(provider, model) }
