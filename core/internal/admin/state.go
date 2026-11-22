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
	Disabled      bool            `json:"disabled"`
	Reasoning     *bool           `json:"reasoning,omitempty"`
	MaxContext    int             `json:"max_context,omitempty"`
	MaxResponse   int             `json:"max_response,omitempty"`
	MaxImages     int             `json:"max_images,omitempty"`
	MaxTools      int             `json:"max_tools,omitempty"`
	WebSearch     bool            `json:"web_search,omitempty"`
	LargeContext  bool            `json:"large_context,omitempty"`
	StrictOrder   bool            `json:"strict_order,omitempty"`
	InactivityMS  int             `json:"inactivity_ms,omitempty"`
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

	ids := cfg.ProviderIDs()
	for id := range cfg.Providers {
		if !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
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
		for _, entry := range entries {
			record.Labels = append(record.Labels, entry.Label)
			if until, err := s.options.Store.Cooldown(ctx, id, provider.BaseURL+"\x00"+entry.Label); err == nil && time.Now().Before(until) {
				if record.Cooldowns == nil {
					record.Cooldowns = map[string]time.Time{}
				}
				record.Cooldowns[entry.Label] = until
			}
		}
		if until, err := s.options.Store.Cooldown(ctx, id, provider.BaseURL); err == nil && time.Now().Before(until) {
			record.CooldownUntil = &until
		}
		view.Providers = append(view.Providers, record)
	}
	for _, route := range cfg.Models {
		record := modelView{
			ID: route.ID, Provider: route.Provider, UpstreamModel: route.UpstreamModel,
			Protocol: route.Protocol, Privacy: route.Privacy, Label: route.CredentialLabel,
			Disabled: route.Disabled, Reasoning: route.Reasoning, MaxContext: route.MaxContext,
			MaxResponse: route.MaxResponse, MaxImages: route.MaxImages,
			MaxTools: route.MaxTools, WebSearch: route.WebSearch,
			LargeContext: route.LargeContext, StrictOrder: route.StrictOrder, InactivityMS: route.InactivityMS,
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
		models, err := webprov.New(60*time.Second).Catalog(r.Context(), credential)
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

// smoke runs every alias that has a credential, sequentially.
func (s *Server) smoke(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Alias string `json:"alias"`
	}
	decode(w, r, &input)
	cfg, err := s.loadConfig()
	if err != nil {
		fail(w, http.StatusInternalServerError, err.Error())
		return
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
		entries, err := s.options.Store.Entries(r.Context(), route.Provider, provider.BaseURL)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			labels[route.Provider] = append(labels[route.Provider], entry.Label)
		}
	}
	models := append([]config.Route{}, cfg.Models...)
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })

	results := make([]probeResult, 0, len(models))
	passed, failed := 0, 0
	for _, route := range models {
		if route.Disabled || (input.Alias != "" && route.ID != input.Alias) {
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
		until, err := s.options.Store.Cooldown(r.Context(), route.Provider, key)
		if bare, bareErr := s.options.Store.Cooldown(r.Context(), route.Provider, provider.BaseURL); bareErr == nil && bare.After(until) {
			until = bare
		}
		if err == nil && time.Now().Before(until) {
			result.Error = "provider cooling until " + until.Format("15:04:05")
		} else {
			result = s.probeOne(r.Context(), cfg, route, "Reply with the single word: ready")
		}
		result.Alias = route.ID
		result.Protocol = string(route.Protocol)
		if result.Protocol == "" {
			result.Protocol = "chat"
		}
		if result.Status == http.StatusOK && result.Text != "" {
			passed++
		} else {
			failed++
		}
		results = append(results, result)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "passed": passed, "failed": failed})
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
func aliasFor(provider, model string) string {
	raw := strings.ToLower(provider + "-" + model)
	var built strings.Builder
	lastDash := false
	for _, r := range raw {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '/' || r == ':' || r == '@'
		if valid {
			built.WriteRune(r)
			lastDash = r == '-'
			continue
		}
		if !lastDash {
			built.WriteByte('-')
			lastDash = true
		}
	}
	alias := strings.Trim(built.String(), "-._:/@")
	if alias == "" {
		alias = "alias"
	}
	if len(alias) > 72 {
		alias = strings.Trim(alias[:72], "-._:/@")
	}
	return alias
}
