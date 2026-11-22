// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestHealthReportsRegressionsOnly(t *testing.T) {
	h := newHealth()
	if regressed := h.record([]probeResult{{Alias: "a", Status: 200, OK: true}}); len(regressed) != 0 {
		t.Fatalf("first run regressed: %+v", regressed)
	}
	regressed := h.record([]probeResult{
		{Alias: "a", Status: 429, Error: "cooling"},
		{Alias: "b", Status: 200, OK: true},
	})
	if len(regressed) != 1 || regressed[0].Alias != "a" {
		t.Fatalf("regression missed: %+v", regressed)
	}
	last, updated, _ := h.snapshot()
	if last["a"].OK || last["b"].OK == false || updated.IsZero() {
		t.Fatalf("state %+v", last)
	}
}

func TestWebhookDelivers(t *testing.T) {
	var body atomic.Pointer[map[string]any]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
			body.Store(&payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	panel := &Server{}
	panel.postWebhook(t.Context(), server.URL, map[string]any{"failed": 1})
	stored := body.Load()
	if stored == nil || (*stored)["failed"] != float64(1) {
		t.Fatalf("webhook payload %+v", stored)
	}
}

func TestScheduledSmokeRecordsHistory(t *testing.T) {
	f := setup(t)
	f.call(t, http.MethodPost, "/api/keys", `{"provider":"fake","label":"work","key":"sk-test"}`, "")
	f.call(t, http.MethodPost, "/api/models",
		`{"provider":"fake","upstream_model":"gpt-x","name":"x-alias","label":"work"}`, "")
	f.panel.runScheduledSmoke(t.Context(), "")
	last, updated, running := f.panel.health.snapshot()
	if updated.IsZero() || running {
		t.Fatal("sweep did not finish")
	}
	if state, ok := last["x-alias"]; !ok || !state.OK {
		t.Fatalf("history %+v", last)
	}
}
