// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"strings"
	"testing"
)

func TestToolNameVerdict(t *testing.T) {
	if status, _, ok := toolNameVerdict(0, 0); ok || status != "" {
		t.Fatalf("quiet parser reported: %q %v", status, ok)
	}
	if status, _, ok := toolNameVerdict(5, 1); !ok || status != "ok" {
		t.Fatalf("repairs mostly worked: %q %v", status, ok)
	}
	if status, detail, ok := toolNameVerdict(1, 5); !ok || status != "warn" || !strings.Contains(detail, "outside the catalog") {
		t.Fatalf("unknown names not flagged: %q %q %v", status, detail, ok)
	}
	if status, _, ok := toolNameVerdict(0, 3); !ok || status != "warn" {
		t.Fatalf("all names unknown: %q %v", status, ok)
	}
}

func TestDiagnoseReportsToolNames(t *testing.T) {
	f := setup(t)
	previous := nameStats
	nameStats = func() (int64, int64) { return 1, 4 }
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
	nameStats = func() (int64, int64) { return 0, 0 }
	t.Cleanup(func() { nameStats = previous })

	for _, check := range Diagnose(context.Background(), f.store, f.path) {
		if check.Name == "tool names" {
			t.Fatalf("tool names reported without traffic: %+v", check)
		}
	}
}
