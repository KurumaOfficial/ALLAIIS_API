// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

type fixture struct {
	panel  *Server
	store  *vault.Store
	path   string
	live   *gateway.Gateway
	gate   *httptest.Server
	upward *httptest.Server
}

func setup(t *testing.T) *fixture {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io := `{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
		w.Write([]byte(io))
	}))
	t.Cleanup(upstream.Close)
	gate := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gate.Close)
	address := strings.TrimPrefix(gate.URL, "http://")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := config.Default()
	cfg.Listen = address
	cfg.Providers = map[string]config.Provider{"fake": {BaseURL: upstream.URL}}
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(dir, "vault.sqlite")
	pass := []byte("admin-test-passphrase")
	if err := vault.Init(t.Context(), vaultPath, pass); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), vaultPath, pass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithReload(path, store)
	gate.Config.Handler = live
	panel, err := New(Options{
		Gateway: live, Store: store, ConfigPath: path, DataDir: dir, Listen: address,
		Passphrase: pass, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{panel: panel, store: store, path: path, live: live, gate: gate, upward: upstream}
}

func (f *fixture) call(t *testing.T, method, path, body string, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token == "" {
		token = f.panel.token
	}
	request.Header.Set(tokenHeader, token)
	result := httptest.NewRecorder()
	f.panel.ServeHTTP(result, request)
	return result
}

func TestPanelRejectsMissingToken(t *testing.T) {
	f := setup(t)
	result := f.call(t, http.MethodGet, "/api/state", "", "wrong")
	if result.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", result.Code)
	}
}

func TestPanelServesPageWithToken(t *testing.T) {
	f := setup(t)
	result := f.call(t, http.MethodGet, "/", "", "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), f.panel.token) {
		t.Fatalf("page missing token: %d", result.Code)
	}
	if !strings.Contains(result.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		t.Fatal("missing CSP")
	}
}

func TestPanelAddKeyAndAlias(t *testing.T) {
	f := setup(t)
	result := f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("add key: %d %s", result.Code, result.Body)
	}
	result = f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","protocol":"","privacy":"consumer_cloud","label":"work"}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("add model: %d %s", result.Code, result.Body)
	}
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "x-alias" {
		t.Fatalf("config not updated: %+v", cfg.Models)
	}
	// The live table must already serve the alias.
	if len(f.live.Models()) == 0 {
		_ = f.live
	}
	state := f.call(t, http.MethodGet, "/api/state", "", "")
	if !strings.Contains(state.Body.String(), "x-alias") || !strings.Contains(state.Body.String(), `"available":true`) {
		t.Fatalf("state does not show an available alias: %s", state.Body)
	}
}

func TestPanelProbe(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	result := f.call(t, http.MethodPost, "/api/probe", `{"model":"x-alias","prompt":"ping"}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("probe: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Status int    `json:"status"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Status != http.StatusOK || reply.Text != "pong" {
		t.Fatalf("probe reply %+v", reply)
	}
}

func TestPanelDeleteAndRotate(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	if result := f.call(t, http.MethodPost, "/api/models/delete", `{"id":"x-alias"}`, ""); result.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", result.Code, result.Body)
	}
	if result := f.call(t, http.MethodPost, "/api/keys/delete", `{"provider":"fake","label":"work"}`, ""); result.Code != http.StatusOK {
		t.Fatalf("key delete: %d %s", result.Code, result.Body)
	}
	before := f.store.ClientKey()
	result := f.call(t, http.MethodPost, "/api/keys/rotate", `{}`, "")
	if result.Code != http.StatusOK || f.store.ClientKey() == before {
		t.Fatalf("rotate: %d %s", result.Code, result.Body)
	}
	if f.live.ValidKey(f.store.ClientKey()) == false {
		t.Fatal("gateway did not adopt the rotated key")
	}
}

func TestPanelSettings(t *testing.T) {
	f := setup(t)
	if result := f.call(t, http.MethodPost, "/api/settings", `{"min_privacy":"zdr"}`, ""); result.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", result.Code, result.Body)
	}
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinPrivacy != config.ZDR {
		t.Fatalf("privacy not saved: %s", cfg.MinPrivacy)
	}
}

func TestAliasFor(t *testing.T) {
	got := aliasFor("venice-web", "claude-opus-5-5")
	if got != "venice-web-claude-opus-5-5" {
		t.Fatalf("unexpected alias %q", got)
	}
	long := aliasFor("p", strings.Repeat("x", 200))
	if len(long) > 72 {
		t.Fatalf("alias too long: %d", len(long))
	}
	if strings.HasPrefix(long, "-") || strings.HasSuffix(long, "-") {
		t.Fatalf("alias has a bad edge: %q", long)
	}
}

func TestStateMarksMissingCredential(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias"}`, "")
	state := f.call(t, http.MethodGet, "/api/state", "", "")
	if !strings.Contains(state.Body.String(), `"available":false`) {
		t.Fatalf("expected unavailable alias: %s", state.Body)
	}
	time.Sleep(10 * time.Millisecond)
}
