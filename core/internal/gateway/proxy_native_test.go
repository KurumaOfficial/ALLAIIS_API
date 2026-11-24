// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
)

// TestNativeProviderUsesProxyPool pins the API-key route: the request must go
// through the saved proxy instead of dialling the provider host, which does not
// resolve in this test.
func TestNativeProviderUsesProxyPool(t *testing.T) {
	var proxied atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied.Store(true)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("proxy saw %s %s", r.Method, r.URL)
		}
		const auth = "Basic YWxpY2U6czNjcmV0"
		if got := r.Header.Get("Proxy-Authorization"); got != auth {
			t.Errorf("proxy authorization %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"via proxy"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`)
	}))
	t.Cleanup(proxy.Close)

	store, cfg := setup(t, "http://provider.invalid")
	handler := newGateway(t, cfg, store)
	live, ok := handler.(*gateway.Gateway)
	if !ok {
		t.Fatalf("unexpected handler %T", handler)
	}
	parsed, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword("alice", "s3cret")
	if count, err := live.SetProxyList(parsed.String()); err != nil || count != 1 {
		t.Fatalf("set authenticated proxy: %d %v", count, err)
	}
	result := complete(handler, store.ClientKey(), "", `{"model":"demo","messages":[{"role":"user","content":"hello"}]}`)
	if result.Code != http.StatusOK || !proxied.Load() {
		t.Fatalf("proxy not used: %d %s", result.Code, result.Body)
	}
	var answer struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &answer); err != nil || len(answer.Choices) == 0 ||
		answer.Choices[0].Message.Content != "via proxy" {
		t.Fatalf("body %s err %v", result.Body, err)
	}
}
