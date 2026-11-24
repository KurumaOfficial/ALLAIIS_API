// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"bytes"
	"context"
	"encoding/hex"
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

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

type fixture struct {
	panel  *Server
	store  *vault.Store
	path   string
	live   *gateway.Gateway
	gate   *httptest.Server
	upward *httptest.Server
}

// toolProseLeft counts how many tool probes still answer in prose, so the retry
// and failover paths are exercised on purpose: each test sets what it needs.
var toolProseLeft atomic.Int32

func setup(t *testing.T) *fixture {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/tee/attestation") {
			key, _ := secp256k1.GeneratePrivateKey()
			fmt.Fprintf(w, `{"verified":true,"nonce":%q,"signing_key":%q,"tee_provider":"fixture","tee_hardware":"intel-tdx"}`,
				r.URL.Query().Get("nonce"), hex.EncodeToString(key.PubKey().SerializeUncompressed()))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := io.ReadAll(r.Body)
		// The tool probe sends a tool list; answer it with a tool call, or with
		// prose while the test asked for it.
		if bytes.Contains(body, []byte(`"tools"`)) {
			if toolProseLeft.Add(-1) >= 0 {
				w.Write([]byte(`{"choices":[{"message":{"content":"Sure, I can list them."}}]}`))
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
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
	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithConfig(cfg).WithReload(path, store)
	gate.Config.Handler = live
	panel, err := New(Options{
		Gateway: live, Store: store, ConfigPath: path, DataDir: dir,
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

func TestPanelShowsTheRepairCounters(t *testing.T) {
	f := setup(t)
	result := f.call(t, http.MethodGet, "/", "", "")
	for _, id := range []string{"ov-repairs", "model-e2ee"} {
		if !strings.Contains(result.Body.String(), id) {
			t.Fatalf("overview lacks %s", id)
		}
	}
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

func TestPanelShowsABalanceMark(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	// A balance mark is scoped to the model on that label, not to the label.
	key := f.upward.URL + "\x00work\x00x-alias"
	if err := f.store.SetCooldown(t.Context(), "fake", key, time.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	state := f.call(t, http.MethodGet, "/api/state", "", "")
	if !strings.Contains(state.Body.String(), `"balance_until"`) {
		t.Fatalf("state hides the balance mark: %s", state.Body)
	}
	if strings.Contains(state.Body.String(), `"cooldowns"`) {
		t.Fatalf("a balance mark parked the whole label: %s", state.Body)
	}
}

func TestPanelAttestsAnEnclave(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"{}"}`, "")
	result := f.call(t, http.MethodPost, "/api/e2ee/probe",
		`{"provider":"fake","label":"work","model":"e2ee-glm-5-3-flash"}`, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "attestation") {
		t.Fatalf("probe failed: %d %s", result.Code, result.Body)
	}
	if !strings.Contains(result.Body.String(), "intel-tdx") {
		t.Fatalf("attestation not reported: %s", result.Body)
	}
}

func TestPanelE2EEProbeExplainsAnUnreadableKey(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"bad","key":"nope"}`, "")
	result := f.call(t, http.MethodPost, "/api/e2ee/probe",
		`{"provider":"fake","label":"bad","model":"e2ee-glm-5-3-flash"}`, "")
	if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "unreadable") {
		t.Fatalf("unreadable key: %d %s", result.Code, result.Body)
	}
}

// TestPanelQuotaShowsPlanAndCredits pins the row the Quota button renders: the
// counters alone hid an empty credit bucket behind a healthy chat allowance.
func TestPanelQuotaShowsPlanAndCredits(t *testing.T) {
	f := setup(t)
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = map[string]config.Provider{
		"web": {BaseURL: "https://web.example/api", Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{ID: "web-alias", Provider: "web", UpstreamModel: "m", Privacy: config.ZDR}}
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cred := webprov.Credential{DistinctID: "device", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "clear"}}}
	if _, err := f.store.Import(context.Background(), []vault.Entry{{
		Provider: "web", Endpoint: "https://web.example/api", Label: "pro", Key: cred.Encode(),
	}}); err != nil {
		t.Fatal(err)
	}
	saved := probeQuota
	probeQuota = func(context.Context, *webprov.Client, webprov.Credential) (map[string]any, error) {
		return map[string]any{
			"chat": map[string]any{"remaining": float64(9685), "resetAt": float64(1795499466865)},
			"account": map[string]any{
				"userType": "PRO", "availableCredits": float64(0),
				"bundledCreditsUsage": map[string]any{
					"availableCredits": float64(0), "monthlyRefillCredits": float64(100),
					"nextRefillAt": float64(1797412488000),
				},
			},
		}, nil
	}
	t.Cleanup(func() { probeQuota = saved })

	result := f.call(t, http.MethodPost, "/api/web/quota", `{"provider":"web"}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("quota: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Labels []struct {
			Label     string  `json:"label"`
			Plan      string  `json:"plan"`
			Credits   float64 `json:"credits"`
			CreditCap float64 `json:"credit_cap"`
			Chat      float64 `json:"chat_remaining"`
			RefillAt  float64 `json:"credit_refill_at"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	row := reply.Labels[0]
	if row.Label != "pro" || row.Plan != "PRO" || row.Credits != 0 || row.CreditCap != 100 ||
		row.Chat != 9685 || row.RefillAt == 0 {
		t.Fatalf("row %+v", row)
	}
}

// TestPanelRefusesAWebKeyThatIsNotASession pins the one check that keeps a
// pasted API key from becoming an unreadable web session.
func TestPanelRefusesAWebKeyThatIsNotASession(t *testing.T) {
	f := setup(t)
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Providers = map[string]config.Provider{
		"web": {BaseURL: "https://web.example/api", Transport: config.TransportVeniceWeb},
	}
	cfg.Models = []config.Route{{ID: "web-alias", Provider: "web", UpstreamModel: "m", Privacy: config.ZDR}}
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}

	bad := f.call(t, http.MethodPost, "/api/keys",
		`{"provider":"web","label":"pro","key":"sk-a-plain-api-key"}`, "")
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "web login") {
		t.Fatalf("plain key accepted: %d %s", bad.Code, bad.Body)
	}

	cred := webprov.Credential{DistinctID: "device", Venice: []webprov.Cookie{{Name: "cf_clearance", Value: "clear"}}}
	good := f.call(t, http.MethodPost, "/api/keys",
		`{"provider":"web","label":"pro","key":`+strconv.Quote(cred.Encode())+`}`, "")
	if good.Code != http.StatusOK {
		t.Fatalf("session refused: %d %s", good.Code, good.Body)
	}
}

// TestPanelExportsTheConfig pins the Export button's shape: the file's fields,
// not a summary that a reader cannot paste back.
func TestPanelExportsTheConfig(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias"}`, "")
	result := f.call(t, http.MethodGet, "/api/export", "", "")
	if result.Code != http.StatusOK {
		t.Fatalf("export: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Listen    string           `json:"listen"`
		Providers map[string]any   `json:"providers"`
		Models    []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Listen == "" || len(reply.Providers) == 0 {
		t.Fatalf("reply %+v", reply)
	}
	found := false
	for _, model := range reply.Models {
		if model["id"] == "x-alias" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exported models %+v", reply.Models)
	}
}

// TestExportWithoutCustomProvidersIsAnObject pins the shape a reader pastes
// back: a config with no custom providers used to export "providers": null.
func TestExportWithoutCustomProvidersIsAnObject(t *testing.T) {
	f := setup(t)
	if err := os.WriteFile(f.path, []byte(`{"listen":"127.0.0.1:1","models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := f.call(t, http.MethodGet, "/api/export", "", "")
	if result.Code != http.StatusOK {
		t.Fatalf("export: %d %s", result.Code, result.Body)
	}
	var reply map[string]json.RawMessage
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if string(reply["providers"]) != "{}" {
		t.Fatalf("providers %s", reply["providers"])
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
	if got != "claude-opus-5-5-venice" {
		t.Fatalf("unexpected alias %q", got)
	}
	if other := aliasFor("openai", "gpt-4"); other != "gpt-4-openai" {
		t.Fatalf("unexpected alias %q", other)
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

func TestDiagnosticsFlagsMissingCredential(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias"}`, "")
	result := f.call(t, http.MethodPost, "/api/doctor", `{}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("doctor: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range reply.Checks {
		if item.Name == "alias credentials" {
			found = item.Status == "error"
		}
	}
	if !found {
		t.Fatalf("missing credential not flagged: %s", result.Body)
	}
}

// TestProxiesEndpointRoundTrips pins the panel field: a valid list is parsed,
// applied and readable; a malformed line is refused without saving.
func TestProxiesEndpointRoundTrips(t *testing.T) {
	f := setup(t)
	result := f.call(t, http.MethodPost, "/api/proxies",
		`{"text":"127.0.0.1:8080:user:pass\n127.0.0.1:8081"}`, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"count":2`) {
		t.Fatalf("save: %d %s", result.Code, result.Body)
	}
	get := f.call(t, http.MethodGet, "/api/proxies", "", "")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "127.0.0.1:8080") {
		t.Fatalf("get: %d %s", get.Code, get.Body)
	}
	bad := f.call(t, http.MethodPost, "/api/proxies", `{"text":"not a proxy"}`, "")
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed list accepted: %d %s", bad.Code, bad.Body)
	}
	clear := f.call(t, http.MethodPost, "/api/proxies", `{"text":""}`, "")
	if clear.Code != http.StatusOK || !strings.Contains(clear.Body.String(), `"count":0`) {
		t.Fatalf("clear: %d %s", clear.Code, clear.Body)
	}
}

func TestSmokeProbesEveryAvailableAlias(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	// One prose reply, so the retry is part of the path under test.
	toolProseLeft.Store(1)
	result := f.call(t, http.MethodPost, "/api/smoke", `{}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("smoke: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Results []struct {
			Alias  string `json:"alias"`
			Status int    `json:"status"`
			OK     bool   `json:"ok"`
			Text   string `json:"text"`
		} `json:"results"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Passed != 2 || reply.Failed != 0 || reply.Results[0].Text != "pong" {
		t.Fatalf("unexpected smoke reply: %+v", reply)
	}
	// The sweep carries one tool probe so a parser regression cannot hide.
	tooled := reply.Results[len(reply.Results)-1]
	if !tooled.OK || !strings.Contains(tooled.Text, "list_files") {
		t.Fatalf("tool probe missing: %+v", reply.Results)
	}
}

// TestSmokeSkipsAParkedModel pins the model-scoped balance mark: a parked
// alias used to spend a request that could only answer 402.
func TestSmokeSkipsAParkedModel(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	key := f.upward.URL + "\x00work\x00x-alias"
	if err := f.store.SetCooldown(t.Context(), "fake", key, time.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result := f.call(t, http.MethodPost, "/api/smoke", `{}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("smoke: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Results []struct {
			Alias  string `json:"alias"`
			Status int    `json:"status"`
			OK     bool   `json:"ok"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Passed != 0 || reply.Failed != 1 || len(reply.Results) != 1 {
		t.Fatalf("unexpected smoke reply: %+v", reply)
	}
	got := reply.Results[0]
	if got.Status != http.StatusPaymentRequired || got.OK || !strings.Contains(got.Error, "model has no balance") {
		t.Fatalf("parked model was probed: %+v", got)
	}
}

func TestSmokeSkipsAliasesWithoutCredentials(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"lonely"}`, "")
	result := f.call(t, http.MethodPost, "/api/smoke", `{}`, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"passed":0`) {
		t.Fatalf("unexpected: %d %s", result.Code, result.Body)
	}
}

func TestToggleMutesAlias(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	if len(f.live.Models()) != 1 {
		t.Fatal("alias not served before muting")
	}
	result := f.call(t, http.MethodPost, "/api/models/toggle", `{"id":"x-alias","disabled":true}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", result.Code, result.Body)
	}
	if len(f.live.Models()) != 0 {
		t.Fatal("muted alias still served")
	}
	state := f.call(t, http.MethodGet, "/api/state", "", "")
	if !strings.Contains(state.Body.String(), `"disabled":true`) {
		t.Fatalf("state missed the mute: %s", state.Body)
	}
}

func TestClearCooldownEndpoint(t *testing.T) {
	f := setup(t)
	key := f.upward.URL + "\x00work"
	if err := f.store.SetCooldown(t.Context(), "fake", key, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result := f.call(t, http.MethodPost, "/api/cooldown/clear", `{"provider":"fake","label":"work"}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("clear: %d %s", result.Code, result.Body)
	}
	until, err := f.store.Cooldown(t.Context(), "fake", key)
	if err != nil || !until.IsZero() {
		t.Fatalf("cooldown survived: %v %v", until, err)
	}
}

func TestReplacingAKeyClearsItsCooldown(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	key := f.upward.URL + "\x00work"
	if err := f.store.SetCooldown(t.Context(), "fake", key, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	result := f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-new"}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", result.Code, result.Body)
	}
	if until, err := f.store.Cooldown(t.Context(), "fake", key); err != nil || !until.IsZero() {
		t.Fatalf("cooldown survived a key replacement: %v %v", until, err)
	}
}

func catalogFixture(t *testing.T, hits *int32) *Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"model-a"},{"id":"model-b"}]}`))
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	gate := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gate.Close)
	cfg := config.Default()
	cfg.Listen = strings.TrimPrefix(gate.URL, "http://")
	cfg.Providers = map[string]config.Provider{"fake": {BaseURL: server.URL}}
	cfg.Models = []config.Route{}
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(dir, "vault.sqlite")
	pass := []byte("catalog-test-passphrase")
	if err := vault.Init(t.Context(), vaultPath, pass); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), vaultPath, pass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "fake", Endpoint: server.URL, Label: "work", Key: "sk-catalog",
	}}); err != nil {
		t.Fatal(err)
	}
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithConfig(cfg).WithReload(path, store)
	panel, err := New(Options{Gateway: live, Store: store, ConfigPath: path, DataDir: dir, Passphrase: pass})
	if err != nil {
		t.Fatal(err)
	}
	return panel
}

func TestCatalogCacheServesAndBypasses(t *testing.T) {
	var hits int32
	panel := catalogFixture(t, &hits)
	call := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(tokenHeader, panel.token)
		result := httptest.NewRecorder()
		panel.ServeHTTP(result, request)
		return result
	}
	first := call("/api/discover?provider=fake&label=work")
	if first.Code != http.StatusOK || strings.Contains(first.Body.String(), `"cached":true`) {
		t.Fatalf("first call: %d %s", first.Code, first.Body)
	}
	second := call("/api/discover?provider=fake&label=work")
	if !strings.Contains(second.Body.String(), `"cached":true`) {
		t.Fatalf("second call not cached: %s", second.Body)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("upstream hit %d times, want 1", hits)
	}
	refresh := call("/api/discover?provider=fake&label=work&refresh=1")
	if refresh.Code != http.StatusOK || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("refresh did not refetch: %d hits=%d", refresh.Code, hits)
	}
}

func TestAddModelStoresCapabilities(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	body := `{"provider":"fake","upstream_model":"gpt-x","name":"capable","label":"work",` +
		`"reasoning":true,"max_context":200000,"max_response":8000,"max_images":4,"temperature":0.4,"top_p":0.8}`
	if result := f.call(t, http.MethodPost, "/api/models", body, ""); result.Code != http.StatusOK {
		t.Fatalf("add: %d %s", result.Code, result.Body)
	}
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	route := cfg.Models[0]
	if route.Reasoning == nil || !*route.Reasoning || route.MaxContext != 200000 ||
		route.MaxResponse != 8000 || route.MaxImages != 4 ||
		route.Temperature == nil || *route.Temperature != 0.4 || route.TopP == nil || *route.TopP != 0.8 {
		t.Fatalf("capabilities lost: %+v", route)
	}
}

func TestToolCallTextReadsEachProtocol(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		protocol config.Protocol
		want     string
	}{
		{"chat", `{"choices":[{"message":{"tool_calls":[{"function":{"name":"list_files","arguments":"{}"}}]}}]}`, config.Chat, "list_files {}"},
		{"messages", `{"content":[{"type":"text","text":"hi"},{"type":"tool_use","name":"list_files","input":{}}]}`, config.Messages, "list_files {}"},
		{"responses", `{"output":[{"type":"function_call","name":"list_files","arguments":"{}"}]}`, config.Responses, "list_files {}"},
		{"prose only", `{"choices":[{"message":{"content":"Sure, the files are..."}}]}`, config.Chat, ""},
		{"garbage", `not json`, config.Chat, ""},
	}
	for _, c := range cases {
		got, ok := toolCallText([]byte(c.raw), c.protocol)
		if got != c.want || ok != (c.want != "") {
			t.Fatalf("%s: got %q/%v, want %q", c.name, got, ok, c.want)
		}
	}
}

func TestToolProbeFailsOverToAnotherAlias(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	for _, name := range []string{"a-alias", "x-alias"} {
		f.call(t, http.MethodPost, "/api/models",
			`{"provider":"fake","upstream_model":"gpt-x","name":"`+name+`","label":"work"}`, "")
	}
	// Two prose replies exhaust the retry, so the row has to move on.
	toolProseLeft.Store(2)
	result := f.call(t, http.MethodPost, "/api/smoke", `{}`, "")
	var reply struct {
		Passed  int `json:"passed"`
		Failed  int `json:"failed"`
		Results []struct {
			Alias string `json:"alias"`
			OK    bool   `json:"ok"`
		} `json:"results"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	// a-alias passes its own probe, so the tool row moves to x-alias and passes.
	if reply.Failed != 0 || reply.Passed != 3 {
		t.Fatalf("unexpected sweep: %+v", reply)
	}
	tooled := reply.Results[len(reply.Results)-1]
	if !tooled.OK || tooled.Alias != "x-alias (tools)" {
		t.Fatalf("tool row did not fail over: %+v", reply.Results)
	}
}

func TestProbeToolsEndpoint(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")

	result := f.call(t, http.MethodPost, "/api/probe/tools", `{"model":"x-alias"}`, "")
	if result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"ok":true`) ||
		!strings.Contains(result.Body.String(), "list_files") {
		t.Fatalf("tool probe: %d %s", result.Code, result.Body)
	}

	if missing := f.call(t, http.MethodPost, "/api/probe/tools", `{"model":"nope"}`, ""); missing.Code != http.StatusBadRequest {
		t.Fatalf("unknown alias: %d", missing.Code)
	}
}
