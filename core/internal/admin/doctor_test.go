// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
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
			found = check.Status == "warn"
		}
	}
	if !found {
		t.Fatalf("tool names not flagged: %+v", checks)
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

func TestFetchLiveStatsParsesTheExposition(t *testing.T) {
	body := strings.Join([]string{
		`allaiis_tool_name_fold_total 3`,
		`allaiis_tool_name_suffix_total 1`,
		`allaiis_tool_unknown_total 5`,
		`allaiis_tool_ttl_seconds 86400`,
		`allaiis_tool_unknown_name_total{name="bash"} 4`,
		`allaiis_tool_unknown_name_age_seconds{name="bash"} 60`,
		`allaiis_tool_unknown_name_total{name="grep"} 1`,
		`allaiis_tool_unknown_name_age_seconds{name="grep"} 200000`,
	}, "\n")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

func TestFetchLiveStatsFailsWhenUnreachable(t *testing.T) {
	if _, ok := fetchLiveStats(context.Background(), "127.0.0.1:1"); ok {
		t.Fatal("unreachable gateway reported ok")
	}
}
