// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

const payload = `{"model":"demo","messages":[{"role":"user","content":"hello"}]}`

func TestRoutingAndProviderCooldown(t *testing.T) {
	var rateLimited atomic.Bool
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Model string `json:"model"`
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil ||
			request.Model != "real-model" || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-allaiis-") {
			t.Error("upstream received incorrect path, model or authorization")
		}
		if rateLimited.Load() {
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, "upstream-private-error")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"object":"chat.completion","fixture_key":%q}`, r.Header.Get("Authorization"))
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	handler := newGateway(t, cfg, store)

	if result := complete(handler, "", "", payload); result.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated completion returned %d", result.Code)
	}
	models := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	models.Header.Set("Authorization", "Bearer "+store.ClientKey())
	catalog := httptest.NewRecorder()
	handler.ServeHTTP(catalog, models)
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), `"id":"demo"`) ||
		strings.Contains(catalog.Body.String(), `"id":"public"`) {
		t.Fatalf("unexpected model catalog: %d %s", catalog.Code, catalog.Body)
	}
	blocked := complete(handler, store.ClientKey(), "", strings.Replace(payload, `"demo"`, `"public"`, 1))
	if blocked.Code != http.StatusNotFound || calls.Load() != 0 {
		t.Fatal("privacy policy allowed an unauthorized upstream call")
	}

	first := complete(handler, store.ClientKey(), "", payload)
	continued := complete(handler, store.ClientKey(), "",
		`{"model":"demo","messages":[{"content":"hello","role":"user"},{"role":"assistant","content":"hi"},{"role":"user","content":"continue"}]}`)
	if first.Code != http.StatusOK || first.Header().Get("X-ALLAIIS-Transport-Tier") != "tier_a" ||
		first.Body.String() != continued.Body.String() {
		t.Fatal("conversation did not stay on its original key")
	}
	keys := make(map[string]bool)
	for i := range 40 {
		result := complete(handler, store.ClientKey(), fmt.Sprintf("test-session-%d", i), payload)
		if result.Code != http.StatusOK {
			t.Fatalf("session %d returned %d", i, result.Code)
		}
		keys[result.Body.String()] = true
	}
	if len(keys) != 2 {
		t.Fatalf("session routing used %d keys, want 2", len(keys))
	}

	rateLimited.Store(true)
	before := calls.Load()
	rate := complete(handler, store.ClientKey(), "limited", payload)
	if rate.Code != http.StatusTooManyRequests || rate.Header().Get("Retry-After") == "" ||
		strings.Contains(rate.Body.String(), "upstream-private-error") || calls.Load() != before+1 {
		t.Fatal("the gateway did not handle the provider's 429 safely")
	}
	restarted := newGateway(t, cfg, store)
	duringPause := complete(restarted, store.ClientKey(), "different-session",
		strings.Replace(payload, `"demo"`, `"pinned"`, 1))
	if duringPause.Code != http.StatusTooManyRequests || calls.Load() != before+1 {
		t.Fatal("a new gateway instance did not honor the pool cooldown")
	}
}

func TestProviderAliasesAndPinnedCredentials(t *testing.T) {
	type expectedRequest struct {
		model string
		key   string
	}
	upstreams := map[string]expectedRequest{
		"/v1/chat/completions":          {model: "deepseek-v4-1-flash-1017", key: "fixture-secret-2"},
		"/opencode/v1/chat/completions": {model: "deepseek-v4.1-flash", key: "workspace-key"},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want, ok := upstreams[r.URL.Path]
		var request struct {
			Model string `json:"model"`
		}
		if !ok || json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != want.model ||
			r.Header.Get("Authorization") != "Bearer "+want.key {
			t.Errorf("unexpected provider request on %s for model %q", r.URL.Path, request.Model)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"model":%q}`, request.Model)
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	const veniceModel = "DeepSeek-v4.1-flash-venice"
	const openCodeModel = "DeepSeek-v4.1-flash-opencode-go"
	const unconfigured = "DeepSeek-v4.1-flash-missing-profile"
	endpoint := upstream.URL + "/opencode/v1"
	cfg.Providers["code-fixture"] = config.Provider{BaseURL: endpoint}
	cfg.Models = []config.Route{
		{ID: veniceModel, Provider: "fixture", UpstreamModel: "deepseek-v4-1-flash-1017", Privacy: config.Consumer, CredentialLabel: "second"},
		{ID: openCodeModel, Provider: "code-fixture", UpstreamModel: "deepseek-v4.1-flash", Privacy: config.Consumer, CredentialLabel: "workspace"},
		{ID: unconfigured, Provider: "fixture", UpstreamModel: "deepseek-v4-1-flash-1017", Privacy: config.Consumer, CredentialLabel: "missing"},
	}
	if _, err := store.Import(t.Context(), []vault.Entry{
		{Provider: "code-fixture", Endpoint: endpoint, Label: "workspace", Key: "workspace-key"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := newGateway(t, cfg, store)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, req)
	list := result.Body.String()
	if !strings.Contains(list, veniceModel) || !strings.Contains(list, openCodeModel) || strings.Contains(list, unconfigured) {
		t.Fatalf("unexpected catalog: %s", list)
	}
	for alias, provider := range map[string]string{veniceModel: "fixture", openCodeModel: "code-fixture"} {
		for _, session := range []string{"first-session", "second-session"} {
			res := complete(handler, store.ClientKey(), session, strings.Replace(payload, "demo", alias, 1))
			if res.Code != http.StatusOK || res.Header().Get("X-ALLAIIS-Model") != alias ||
				res.Header().Get("X-ALLAIIS-Provider") != provider {
				t.Fatalf("request for %q returned %d with headers %v", alias, res.Code, res.Header())
			}
		}
	}
	res := complete(handler, store.ClientKey(), "", strings.Replace(payload, "demo", unconfigured, 1))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing pinned profile returned %d instead of 503", res.Code)
	}
}

func TestStreamFlushAndCancel(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, event string
		protocol                config.Protocol
	}{
		{"chat", "/v1/chat/completions", `"messages":[{"role":"user","content":"hello"}]`,
			"data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n", config.Chat},
		{"responses", "/v1/responses", `"input":"hello"`,
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"first\"}\n\n", config.Responses},
		{"messages", "/v1/messages", `"messages":[{"role":"user","content":"hello"}],"max_tokens":20`,
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"first\"}}\n\n", config.Messages},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkStreamFlushAndCancel(t, tc.path, tc.body, tc.event, tc.protocol)
		})
	}
}

func checkStreamFlushAndCancel(t *testing.T, path, payloadFields, event string, protocol config.Protocol) {
	t.Helper()
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("streaming request did not reach the expected endpoint with SSE")
		}
		if protocol == config.Messages && r.Header.Get("Anthropic-Version") != "2026-10-21" {
			t.Error("Messages request did not receive the default API version")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, event)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(upstreamCanceled)
		case <-time.After(5 * time.Second):
			t.Error("upstream stream was still running after the client canceled")
		}
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	cfg.Models = append(cfg.Models, config.Route{
		ID: "stream", Provider: "fixture", UpstreamModel: "real-model", Privacy: config.Consumer,
		Protocol: protocol, CredentialLabel: "first",
	})
	server := httptest.NewServer(newGateway(t, cfg, store))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body := strings.NewReader(`{"model":"stream","stream":true,` + payloadFields + "}")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("unexpected stream response: %d %v", response.StatusCode, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	var got strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("first event was not forwarded live: %v", err)
		}
		got.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if got.String() != event {
		t.Fatalf("first event changed: got %q, want %q", got.String(), event)
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach upstream")
	}
}

func TestUpstreamRedirectIsNotFollowed(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("gateway followed the upstream redirect")
	}))
	t.Cleanup(receiver.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, receiver.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	result := complete(newGateway(t, cfg, store), store.ClientKey(), "", payload)
	if result.Code != http.StatusBadGateway || result.Header().Get("Location") != "" {
		t.Fatalf("unexpected response to upstream redirect: %d", result.Code)
	}
}

func setup(t *testing.T, upstream string) (*vault.Store, config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data & vault.sqlite")
	passphrase := []byte("test-vault-passphrase")
	if err := vault.Init(t.Context(), path, passphrase); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), path, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	endpoint := upstream + "/v1"
	_, err = store.Import(t.Context(), []vault.Entry{
		{Provider: "fixture", Endpoint: endpoint, Label: "first", Key: "fixture-secret-1"},
		{Provider: "fixture", Endpoint: endpoint, Label: "second", Key: "fixture-secret-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	cleartextCheck, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cleartextCheck), "fixture-secret-1") ||
		strings.Contains(string(cleartextCheck), store.ClientKey()) {
		t.Fatal("vault leaked a plaintext API key")
	}
	return store, config.Config{
		Listen: config.DefaultListen, MinPrivacy: config.Consumer,
		Providers: map[string]config.Provider{"fixture": {BaseURL: endpoint}},
		Models: []config.Route{
			{ID: "demo", Provider: "fixture", UpstreamModel: "real-model", Privacy: config.Consumer},
			{ID: "public", Provider: "fixture", UpstreamModel: "real-model", Privacy: config.Public},
			{ID: "pinned", Provider: "fixture", UpstreamModel: "real-model", Privacy: config.Consumer, CredentialLabel: "second"},
		},
	}
}

func newGateway(t *testing.T, cfg config.Config, store *vault.Store) http.Handler {
	t.Helper()
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	return gateway.New(routes, store.ClientKey(), cfg.MinPrivacy)
}

func complete(handler http.Handler, key, session, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-ALLAIIS-Session", session)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}
