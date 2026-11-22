// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// veniceFixture serves the attestation and inference calls a web provider makes.
func veniceFixture(t *testing.T, reply func(w http.ResponseWriter, body map[string]any)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"tokens":[{"token":"attestation"}]}`)
		case "/inference/chat":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			reply(w, body)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func webGateway(t *testing.T, upstream string) (http.Handler, *vault.Store) {
	t.Helper()
	store, _ := setup(t, upstream)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{
		"venice-web-fixture": {BaseURL: upstream, Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{
		ID: "venice-uncensored-venice-web-fixture", Provider: "venice-web-fixture",
		UpstreamModel: "venice-uncensored-1-2", Privacy: config.ZDR,
	}}
	cred := webprov.Credential{DistinctID: "device", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "clear"}}}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "venice-web-fixture", Endpoint: upstream, Label: "personal", Key: cred.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	web := webprov.New(5 * time.Second).WithHTTP(http.DefaultClient).WithBase(upstream)
	return gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web), store
}

const webModel = "venice-uncensored-venice-web-fixture"

func TestWebCompletionStreams(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if body["modelId"] != "venice-uncensored-1-2" || body["reasoning"] != false {
			t.Errorf("unexpected payload %v", body)
		}
		fmt.Fprint(w, `{"kind":"meta","servingModelId":"venice-uncensored-1-2"}
{"kind":"content","content":"Hello"}
{"kind":"content","content":", world."}
`)
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "",
		`{"model":"`+webModel+`","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"stream":true}`)
	body := result.Body.String()
	if result.Code != http.StatusOK || result.Header().Get("X-ALLAIIS-Transport-Tier") != "tier_c3" ||
		result.Header().Get("X-ALLAIIS-Emulated-Features") != "usage_estimate" ||
		!strings.Contains(body, `"content":"Hello`) || !strings.Contains(body, `"finish_reason":"stop"`) ||
		!strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("unexpected stream: %d %s", result.Code, body)
	}
}

func TestWebCompletionJSON(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fmt.Fprint(w, `{"kind":"content","content":"full answer"}`)
	})
	handler, store := webGateway(t, server.URL)
	result := complete(handler, store.ClientKey(), "",
		`{"model":"`+webModel+`","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Estimated bool `json:"estimated"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%v: %s", err, result.Body)
	}
	if result.Code != http.StatusOK || reply.Choices[0].Message.Content != "full answer" || !reply.Usage.Estimated {
		t.Fatalf("unexpected reply: %d %s", result.Code, result.Body)
	}
}

func TestWebCompletionErrorsAndCooldown(t *testing.T) {
	var limited atomic.Bool
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		if limited.Load() {
			w.Header().Set("x-ratelimit-resets", fmt.Sprint(time.Now().Add(3*time.Minute).UnixMilli()))
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, "<html>challenge</html>")
	})
	handler, store := webGateway(t, server.URL)
	request := `{"model":"` + webModel + `","messages":[{"role":"user","content":"hi"}]}`
	denied := complete(handler, store.ClientKey(), "", request)
	if denied.Code != http.StatusBadGateway || !strings.Contains(denied.Body.String(), "cloudflare_challenge") {
		t.Fatalf("403 mapped to %d %s", denied.Code, denied.Body.String())
	}
	limited.Store(true)
	rate := complete(handler, store.ClientKey(), "s", request)
	if rate.Code != http.StatusTooManyRequests || rate.Header().Get("Retry-After") == "" {
		t.Fatalf("429 mapped to %d", rate.Code)
	}
	if seconds, _ := strconv.Atoi(rate.Header().Get("Retry-After")); seconds < 120 {
		t.Fatalf("published reset was ignored, Retry-After=%q", rate.Header().Get("Retry-After"))
	}
	blocked := complete(handler, store.ClientKey(), "other", request)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("cooldown not honored: %d", blocked.Code)
	}
}

func TestWebProviderRejectsOtherProtocols(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		io.WriteString(w, `{"kind":"content","content":"x"}`)
	})
	store, _ := setup(t, server.URL)
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"web": {BaseURL: server.URL, Transport: config.TransportVeniceWeb}}
	cfg.Models = []config.Route{{ID: "alias-web", Provider: "web", UpstreamModel: "m", Privacy: config.Consumer}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Models[0].Protocol = config.Responses
	if err := cfg.Validate(); err == nil {
		t.Fatal("responses protocol accepted for a web provider")
	}
	cfg.Models[0].Protocol = config.Chat
	if _, err := store.Import(t.Context(), []vault.Entry{{Provider: "web", Endpoint: server.URL, Label: "l", Key: "{}"}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	handler := gateway.NewWithWeb(routes, store.ClientKey(), config.Consumer,
		webprov.New(time.Second).WithHTTP(http.DefaultClient).WithBase(server.URL))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"alias-web","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"content":"x"`) {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}

func TestWebVisionUsesAgentMultipart(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("pngbytes"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/mint"):
			io.WriteString(w, `{"tokens":[{"token":"t"}]}`)
		case r.URL.Path == "/inference/chat/agents/venice":
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				t.Errorf("content type %q", r.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "input_image") || !strings.Contains(string(body), "pngbytes") {
				t.Errorf("image missing from body")
			}
			io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"Red\"}\n\ndata: [DONE]\n\n")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	handler, store := webGateway(t, server.URL)
	payload := `{"model":"` + webModel + `","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"colour?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + png + `"}}]}]}`
	result := complete(handler, store.ClientKey(), "", payload)
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "Red") {
		t.Fatalf("got %d %s", result.Code, result.Body.String())
	}
}
