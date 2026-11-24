// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	var calls atomic.Int32
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
		if calls.Add(1) > 1 {
			return
		}
		select {
		case <-r.Context().Done():
			fmt.Fprint(w, "data: late-byte-from-canceled-request\n\n")
			w.(http.Flusher).Flush()
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
	next, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path,
		strings.NewReader(`{"model":"stream","stream":true,`+payloadFields+"}"))
	if err != nil {
		t.Fatal(err)
	}
	next.Header.Set("Authorization", "Bearer "+store.ClientKey())
	nextReply, err := server.Client().Do(next)
	if err != nil {
		t.Fatal(err)
	}
	defer nextReply.Body.Close()
	nextEvent, err := io.ReadAll(nextReply.Body)
	if err != nil || nextReply.StatusCode != http.StatusOK || string(nextEvent) != event {
		t.Fatalf("canceled request affected the next stream: %d %q (%v)", nextReply.StatusCode, nextEvent, err)
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
	return gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithVersion("test-build")
}

func complete(handler http.Handler, key, session, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-ALLAIIS-Session", session)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	return result
}

func TestRequestIDIsEchoedAndStamped(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	request.Header.Set("X-Request-Id", "trace-123")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if got := result.Header().Get("X-Request-Id"); got != "trace-123" {
		t.Fatalf("request id %q", got)
	}
}

// TestModelsCarryLimits pins the keys a client uses to size a prompt: the
// alias limits travel under the names such clients look for.
func TestModelsCarryLimits(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	for index := range cfg.Models {
		cfg.Models[index].MaxContext = 64000
		cfg.Models[index].MaxResponse = 8000
	}
	handler := newGateway(t, cfg, store)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	body := result.Body.String()
	if result.Code != http.StatusOK || !strings.Contains(body, `"context_length":64000`) ||
		!strings.Contains(body, `"max_output_tokens":8000`) {
		t.Fatalf("limits missing: %d %s", result.Code, body)
	}
}

// TestModelsAnswerEitherHeaderAndOneAlias pins what a client does before a long
// run: read the list with its own header style, then ask about one alias.
func TestModelsAnswerEitherHeaderAndOneAlias(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)

	one := httptest.NewRequest(http.MethodGet, "/v1/models/demo", nil)
	one.Header.Set("X-Api-Key", store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, one)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"id":"demo"`) {
		t.Fatalf("single model: %d %s", result.Code, result.Body)
	}

	list := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	list.Header.Set("X-Api-Key", store.ClientKey())
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, list)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"id":"demo"`) {
		t.Fatalf("list with x-api-key: %d %s", result.Code, result.Body)
	}

	missing := httptest.NewRequest(http.MethodGet, "/v1/models/nope", nil)
	missing.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, missing)
	if result.Code != http.StatusNotFound || !strings.Contains(result.Body.String(), "model_not_found") {
		t.Fatalf("missing model: %d %s", result.Code, result.Body)
	}
}

// TestModelsAnswerTheAnthropicShape pins the same path answering the shape a
// Claude client parses when it sends its version header.
func TestModelsAnswerTheAnthropicShape(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	cfg.Models = append(cfg.Models, config.Route{
		ID: "demo-msg", Provider: "fixture", UpstreamModel: "real-model",
		Privacy: config.Consumer, Protocol: config.Messages,
	})
	handler := newGateway(t, cfg, store)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("X-Api-Key", store.ClientKey())
	request.Header.Set("Anthropic-Version", "2023-06-01")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	body := result.Body.String()
	if result.Code != http.StatusOK || !strings.Contains(body, `"type":"model"`) ||
		!strings.Contains(body, `"display_name":"demo-msg"`) || !strings.Contains(body, `"has_more":false`) {
		t.Fatalf("anthropic list: %d %s", result.Code, body)
	}
	if strings.Contains(body, `"id":"demo"`) {
		t.Fatalf("a Chat alias is offered to a Messages caller: %s", body)
	}
	// The OpenAI shape keeps the Chat aliases and hides the Messages one.
	list := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	list.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result = httptest.NewRecorder()
	handler.ServeHTTP(result, list)
	if strings.Contains(result.Body.String(), `"id":"demo-msg"`) {
		t.Fatalf("a Messages alias is offered to a Chat caller: %s", result.Body)
	}
}

// TestTwoCredentialHeadersAreRefused pins the debugging trap: a request with
// both headers used to answer a bare 401, which reads as a wrong key.
func TestTwoCredentialHeadersAreRefused(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	request.Header.Set("X-Api-Key", store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "ambiguous_credentials") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

// TestHealthNamesTheBuild pins the one line a monitor or a bug report reads: the
// process was healthy, but nothing said which binary answered.
func TestHealthNamesTheBuild(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"version":"test-build"`) {
		t.Fatalf("health: %d %s", result.Code, result.Body)
	}
}

// TestInvalidUTF8IsRefused pins a silent corruption: Go's decoder replaces bad
// bytes with U+FFFD and the model answers text the caller never sent.
func TestInvalidUTF8IsRefused(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	body := append([]byte(`{"model":"demo","messages":[{"role":"user","content":"`), 0xFF)
	body = append(body, []byte(`"}]}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "valid UTF-8") {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

// TestParseErrorNamesTheMistake pins the message a caller reads after a broken
// body: "expected a JSON object" was printed for a truncated document too.
func TestParseErrorNamesTheMistake(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	for _, tc := range []struct{ name, body, want string }{
		{"truncated", `{"model":"demo","messages":[{"role":"user","content":"hi"}]`, "not valid JSON"},
		{"array", `[]`, "must be a JSON object"},
		{"null", `null`, "must be a JSON object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+store.ClientKey())
			result := httptest.NewRecorder()
			handler.ServeHTTP(result, request)
			if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), tc.want) {
				t.Fatalf("got %d %s", result.Code, result.Body)
			}
		})
	}
}

// TestUTF8BOMIsAccepted pins a Windows habit: a BOM before the JSON used to be
// a syntax error, though the document after it is fine.
func TestUTF8BOMIsAccepted(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	body := append([]byte{0xEF, 0xBB, 0xBF},
		[]byte(`{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`)...)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusNotFound {
		t.Fatalf("got %d %s", result.Code, result.Body)
	}
}

func TestInvalidRequestIDIsReplaced(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	request.Header.Set("X-Request-Id", "bad id!")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	got := result.Header().Get("X-Request-Id")
	if !strings.HasPrefix(got, "req_") {
		t.Fatalf("invalid id kept: %q", got)
	}
}

func TestErrorBodyCarriesRequestID(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("X-Request-Id", "trace-err")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusUnauthorized || !strings.Contains(result.Body.String(), `"request_id":"trace-err"`) {
		t.Fatalf("no request id in error: %d %s", result.Code, result.Body)
	}
}

func TestPreflightAndCORSHeaders(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	preflight := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	preflight.Header.Set("Origin", "http://localhost:3000")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, preflight)
	if result.Code != http.StatusNoContent || result.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight: %d %v", result.Code, result.Header())
	}
	if !strings.Contains(result.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("authorization not allowed: %q", result.Header().Get("Access-Control-Allow-Headers"))
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Header().Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(response.Header().Get("Access-Control-Expose-Headers"), "X-Request-Id") {
		t.Fatalf("cors headers missing: %v", response.Header())
	}
}

func TestMetricsExposition(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"hello"}}]}`)
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	handler := newGateway(t, cfg, store)

	completion := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"demo","messages":[{"role":"user","content":"hi"}]}`))
	completion.Header.Set("Authorization", "Bearer "+store.ClientKey())
	handler.ServeHTTP(httptest.NewRecorder(), completion)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics: %d %s", result.Code, result.Header().Get("Content-Type"))
	}
	body := result.Body.String()
	if !strings.Contains(body, `allaiis_requests_total{model="demo",protocol="chat",status="200"} 1`) {
		t.Fatalf("series missing: %s", body)
	}
	if !strings.Contains(body, "allaiis_models_served 3") || !strings.Contains(body, "allaiis_up 1") {
		t.Fatalf("gauges missing: %s", body)
	}
	for _, line := range []string{
		"allaiis_tool_unwrap_total ", "allaiis_tool_name_fold_total ",
		"allaiis_tool_name_suffix_total ", "allaiis_tool_unknown_total ",
		// Retention and expiry are the context the counters need.
		"allaiis_tool_name_ttl_seconds ", "allaiis_tool_name_expired ",
	} {
		if !strings.Contains(body, line) {
			t.Fatalf("tool counter missing (%s): %s", line, body)
		}
	}
}

func TestMetricsNeedsNoKey(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	handler := newGateway(t, cfg, store)
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if result.Code != http.StatusOK {
		t.Fatalf("metrics behind auth: %d", result.Code)
	}
}

func TestHugeRetryAfterIsCapped(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "86400")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(upstream.Close)
	store, cfg := setup(t, upstream.URL)
	handler := newGateway(t, cfg, store)
	rate := complete(handler, store.ClientKey(), "", payload)
	if rate.Code != http.StatusTooManyRequests {
		t.Fatalf("429 mapped to %d", rate.Code)
	}
	seconds, err := strconv.Atoi(rate.Header().Get("Retry-After"))
	if err != nil || seconds > 600 {
		t.Fatalf("cooldown was not capped: %q", rate.Header().Get("Retry-After"))
	}
}

func TestRequestCapComesFromTheConfig(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	cfg.MaxRequestMB = 1
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithConfig(cfg)

	big := strings.Repeat("a", 1<<20+1024)
	body := `{"model":"demo","messages":[{"role":"user","content":"` + big + `"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)

	if result.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body accepted: %d", result.Code)
	}
	if !strings.Contains(result.Body.String(), "1 MiB") {
		t.Fatalf("cap not stated: %s", result.Body)
	}
}
