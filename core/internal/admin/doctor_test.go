// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

func TestToolNameVerdict(t *testing.T) {
	if status, _, ok := toolNameVerdict(0, 0, nil); ok || status != "" {
		t.Fatalf("quiet parser reported: %q %v", status, ok)
	}
	if status, _, ok := toolNameVerdict(5, 1, nil); !ok || status != "ok" {
		t.Fatalf("repairs mostly worked: %q %v", status, ok)
	}
	status, detail, ok := toolNameVerdict(1, 5, []string{"bash", "grep"})
	if !ok || status != "warn" || !strings.Contains(detail, "outside the catalog") {
		t.Fatalf("unknown names not flagged: %q %q %v", status, detail, ok)
	}
	if !strings.Contains(detail, "bash, grep") {
		t.Fatalf("names not listed: %q", detail)
	}
	if status, _, ok := toolNameVerdict(0, 3, nil); !ok || status != "warn" {
		t.Fatalf("all names unknown: %q %v", status, ok)
	}
}

func TestDiagnoseNamesE2EEAliases(t *testing.T) {
	f := setup(t)
	has := func() bool {
		for _, check := range Diagnose(context.Background(), f.store, f.path) {
			if check.Name == "e2ee models" {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("a plain alias was called e2ee")
	}
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models = append(cfg.Models, config.Route{
		ID: "e2ee-alias", Provider: "fake", UpstreamModel: "e2ee-glm-5-3-flash",
	})
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	verdict := func() *Check {
		for _, check := range Diagnose(context.Background(), f.store, f.path) {
			if check.Name == "e2ee models" {
				return &check
			}
		}
		return nil
	}
	check := verdict()
	if check == nil || check.Status != "warn" || !strings.Contains(check.Detail, "plaintext") {
		t.Fatalf("e2ee alias not flagged honestly: %+v", check)
	}
	// Turning the alias on changes the report to a pass.
	cfg.Models[len(cfg.Models)-1].E2EE = true
	body, _ = json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if check = verdict(); check == nil || check.Status != "ok" || !strings.Contains(check.Detail, "encrypt") {
		t.Fatalf("enabled alias not acknowledged: %+v", check)
	}
}

func TestDiagnoseAttestsTheEnclave(t *testing.T) {
	f := setup(t)
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models = append(cfg.Models, config.Route{
		ID: "e2ee-alias", Provider: "fake", UpstreamModel: "e2ee-glm-5-3-flash", E2EE: true,
	})
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	provider, _ := cfg.Provider("fake")
	if _, err := f.store.Import(context.Background(), []vault.Entry{{
		Provider: "fake", Endpoint: provider.BaseURL, Label: "work",
		Key: `{"clerk":[{"name":"__client","value":"x"}]}`,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "e2ee enclave" {
			if check.Status != "ok" || !strings.Contains(check.Detail, "attested") {
				t.Fatalf("enclave not attested: %+v", check)
			}
			return
		}
	}
	t.Fatal("no enclave check")
}

func TestDiagnoseWarnsAboutAGuestEnclave(t *testing.T) {
	f := setup(t)
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Models = append(cfg.Models, config.Route{
		ID: "e2ee-alias", Provider: "fake", UpstreamModel: "e2ee-glm-5-3-flash", E2EE: true,
	})
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	provider, _ := cfg.Provider("fake")
	if _, err := f.store.Import(context.Background(), []vault.Entry{{
		Provider: "fake", Endpoint: provider.BaseURL, Label: "work", Key: "{}",
	}}); err != nil {
		t.Fatal(err)
	}
	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "e2ee enclave" {
			if check.Status != "warn" || !strings.Contains(check.Detail, "guest session") {
				t.Fatalf("guest session not flagged: %+v", check)
			}
			return
		}
	}
	t.Fatal("no enclave check")
}

func TestDiagnoseWarnsAboutASmallBodyCap(t *testing.T) {
	f := setup(t)
	cfg, err := config.Load(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRequestBytes() != config.DefaultMaxRequestMB<<20 {
		t.Fatalf("default cap %d", cfg.MaxRequestBytes())
	}
	cfg.Models = append(cfg.Models, config.Route{
		ID: "album", Provider: "fake", UpstreamModel: "m", MaxImages: 10,
	})
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(f.path, body, 0600); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "request cap" {
			found = true
			if check.Status != "warn" || !strings.Contains(check.Detail, "16") {
				t.Fatalf("cap warning: %+v", check)
			}
		}
	}
	if !found {
		t.Fatal("no cap warning")
	}
}

func TestCooldownVerdict(t *testing.T) {
	if _, _, ok := cooldownVerdict(nil); ok {
		t.Fatal("an empty pool reported cooldowns")
	}
	until := time.Now().Add(90 * time.Minute)
	one := []router.ProviderStatus{{
		Provider: "venice-web", Credentials: []string{"guest", "pro"},
		Cooldowns: map[string]time.Time{"guest": until},
	}}
	status, detail, ok := cooldownVerdict(one)
	if !ok || status != "ok" || !strings.Contains(detail, "guest") || !strings.Contains(detail, "1h30m") {
		t.Fatalf("a parked label is not reported: %q %q %v", status, detail, ok)
	}
	all := []router.ProviderStatus{{
		Provider: "venice-web", Credentials: []string{"guest"},
		Cooldowns: map[string]time.Time{"guest": until},
	}}
	if status, detail, ok := cooldownVerdict(all); !ok || status != "warn" || !strings.Contains(detail, "every label is parked") {
		t.Fatalf("a fully parked pool is not a warning: %q %q %v", status, detail, ok)
	}
	expired := []router.ProviderStatus{{
		Provider: "venice-web", Credentials: []string{"guest"},
		Cooldowns: map[string]time.Time{"guest": time.Now().Add(-time.Minute)},
	}}
	if _, _, ok := cooldownVerdict(expired); ok {
		t.Fatal("an elapsed cooldown was reported")
	}
}

func TestDiagnoseReportsToolNames(t *testing.T) {
	f := setup(t)
	previous, previousLive := nameStats, fetchStats
	nameStats = func() (int64, int64, []string) { return 1, 4, []string{"bash"} }
	fetchStats = func(context.Context, string) (liveStats, bool) { return liveStats{}, false }
	t.Cleanup(func() { nameStats, fetchStats = previous, previousLive })

	checks := Diagnose(context.Background(), f.store, f.path)
	found := false
	for _, check := range checks {
		if check.Name == "tool names" {
			// The retention decides how long the warning lives, so it is stated.
			found = check.Status == "warn" && strings.Contains(check.Detail, "retention ")
		}
	}
	if !found {
		t.Fatalf("tool names not flagged with retention: %+v", checks)
	}
}

func TestLiveRetentionOverridesTheConfig(t *testing.T) {
	t.Setenv("ALLAIIS_TOOL_NAME_TTL", "90s")
	if got := liveTTL(30 * 24 * time.Hour); got != 90*time.Second {
		t.Fatalf("override ignored: %v", got)
	}
	if got := retentionNote(0); !strings.Contains(got, "off") {
		t.Fatalf("off not stated: %q", got)
	}
}

func TestDiagnoseSkipsToolNamesWithoutTraffic(t *testing.T) {
	f := setup(t)
	previous, previousLive := nameStats, fetchStats
	nameStats = func() (int64, int64, []string) { return 0, 0, nil }
	fetchStats = func(context.Context, string) (liveStats, bool) { return liveStats{}, false }
	t.Cleanup(func() { nameStats, fetchStats = previous, previousLive })

	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "tool names" {
			t.Fatalf("tool names reported without traffic: %+v", check)
		}
	}
}

func TestToolResetEndpoint(t *testing.T) {
	f := setup(t)
	reset := f.call(t, http.MethodPost, "/api/tools/reset", `{}`, "")
	if reset.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", reset.Code, reset.Body)
	}
	if _, unknown := gateway.ToolNameStats(); unknown != 0 {
		t.Fatalf("record kept: %d", unknown)
	}
}

func TestToolResetNeedsTheToken(t *testing.T) {
	f := setup(t)
	if result := f.call(t, http.MethodPost, "/api/tools/reset", `{}`, "wrong"); result.Code != http.StatusUnauthorized {
		t.Fatalf("reset without a token: %d", result.Code)
	}
}

func TestToolResetAcceptsTheExpiredFlag(t *testing.T) {
	f := setup(t)
	result := f.call(t, http.MethodPost, "/api/tools/reset", `{"expired":true}`, "")
	if result.Code != http.StatusOK {
		t.Fatalf("expired reset: %d %s", result.Code, result.Body)
	}
	if !strings.Contains(result.Body.String(), "dropped") {
		t.Fatalf("no dropped field: %s", result.Body)
	}
}

func TestToolResetToleratesAnEmptyBody(t *testing.T) {
	f := setup(t)
	if result := f.call(t, http.MethodPost, "/api/tools/reset", "", ""); result.Code != http.StatusOK {
		t.Fatalf("empty body: %d %s", result.Code, result.Body)
	}
}

func TestDiagnosePrefersTheLiveGateway(t *testing.T) {
	f := setup(t)
	previous, previousLive := nameStats, fetchStats
	// The local process knows nothing; the gateway knows plenty.
	nameStats = func() (int64, int64, []string) { return 0, 0, nil }
	fetchStats = func(context.Context, string) (liveStats, bool) {
		return liveStats{repaired: 0, unknown: 5, names: []string{"bash"}}, true
	}
	t.Cleanup(func() { nameStats, fetchStats = previous, previousLive })

	found := false
	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "tool names" {
			found = check.Status == "warn" && strings.Contains(check.Detail, "bash")
		}
	}
	if !found {
		t.Fatal("live counters ignored")
	}
}

func TestFetchLiveStatsReadsTheEndpoint(t *testing.T) {
	body := `{"repaired":4,"unknown":5,"ttl_seconds":86400,"names":[` +
		`{"name":"bash","count":4,"age_seconds":60},` +
		`{"name":"grep","count":1,"age_seconds":200000}]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tool-names" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	defer upstream.Close()

	stats, ok := fetchLiveStats(context.Background(), strings.TrimPrefix(upstream.URL, "http://"))
	if !ok {
		t.Fatal("fetch failed")
	}
	if stats.repaired != 4 || stats.unknown != 5 || stats.ttl != 24*time.Hour {
		t.Fatalf("scalars: %+v", stats)
	}
	// grep is past the one-day retention and must not be listed.
	if len(stats.names) != 1 || stats.names[0] != "bash" {
		t.Fatalf("names: %v", stats.names)
	}
	if stats.expired != 1 {
		t.Fatalf("expired not counted: %d", stats.expired)
	}
}

func TestFetchLiveStatsRejectsGarbage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))
	defer upstream.Close()
	if _, ok := fetchLiveStats(context.Background(), strings.TrimPrefix(upstream.URL, "http://")); ok {
		t.Fatal("garbage accepted")
	}
}

func TestFetchLiveStatsFailsWhenUnreachable(t *testing.T) {
	if _, ok := fetchLiveStats(context.Background(), "127.0.0.1:1"); ok {
		t.Fatal("unreachable gateway reported ok")
	}
}

func TestDiagnoseMentionsExpiredNames(t *testing.T) {
	f := setup(t)
	previous, previousLive := nameStats, fetchStats
	nameStats = func() (int64, int64, []string) { return 0, 0, nil }
	fetchStats = func(context.Context, string) (liveStats, bool) {
		return liveStats{unknown: 4, names: []string{"bash"}, expired: 2}, true
	}
	t.Cleanup(func() { nameStats, fetchStats = previous, previousLive })

	found := false
	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "tool names" {
			found = strings.Contains(check.Detail, "2 older name(s) expired")
		}
	}
	if !found {
		t.Fatal("expired names not mentioned")
	}
}

func TestDiagnoseWarnsWhenAutostartIsMissing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the startup folder is Windows-only")
	}
	appData := t.TempDir()
	t.Setenv("APPDATA", appData)
	t.Setenv("ALLAIIS_DATA_DIR", t.TempDir())
	f := setup(t)

	status := func() string {
		for _, check := range Diagnose(context.Background(), f.store, f.path) {
			if check.Name == "autostart" {
				return check.Status
			}
		}
		return ""
	}
	if got := status(); got != "warn" {
		t.Fatalf("missing autostart not flagged: %q", got)
	}

	startup := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	if err := os.MkdirAll(startup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(startup, "ALLAIIS-Gateway.cmd"), []byte("@echo off"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "ok" {
		t.Fatalf("autostart not accepted: %q", got)
	}
}
