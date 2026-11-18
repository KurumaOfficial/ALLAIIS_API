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
	duringPause := complete(restarted, store.ClientKey(), "different-session", payload)
	if duringPause.Code != http.StatusTooManyRequests || calls.Load() != before+1 {
		t.Fatal("a new gateway instance did not honor the pool cooldown")
	}
}

func TestStreamFlushAndCancel(t *testing.T) {
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Error("streaming request did not negotiate SSE")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
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
	server := httptest.NewServer(newGateway(t, cfg, store))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body := strings.NewReader(strings.Replace(payload, `{"model"`, `{"stream":true,"model"`, 1))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", body)
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
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || !strings.Contains(line, `"first"`) {
		t.Fatalf("first event was not forwarded live: %q (%v)", line, err)
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
