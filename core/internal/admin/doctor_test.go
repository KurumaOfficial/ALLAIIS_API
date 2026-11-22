// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

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
	previous := nameStats
	nameStats = func() (int64, int64, []string) { return 1, 4, []string{"bash"} }
	t.Cleanup(func() { nameStats = previous })

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
	previous := nameStats
	nameStats = func() (int64, int64, []string) { return 0, 0, nil }
	t.Cleanup(func() { nameStats = previous })

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
