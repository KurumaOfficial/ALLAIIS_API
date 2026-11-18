// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompletionLogRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "completion.log")
	file, err := openRotatingLog(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	log := slog.New(slog.NewJSONHandler(file, nil))
	for _, value := range []string{"expired", "previous", "current"} {
		log.Info("completion", "origin", value, "model", modelID)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	for filename, want := range map[string]string{path: "current", path + ".1": "previous"} {
		body, err := os.ReadFile(filename)
		var data map[string]any
		if err != nil || json.Unmarshal(body, &data) != nil || data["origin"] != want {
			t.Fatalf("%s: got %s (%v), want %q", filename, body, err, want)
		}
	}
}

func TestWorkerUsageNULAndLog(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = newBridge(server.Listener.Addr().String(), fixtureAPIKey, fixtureWorkerKey, time.Second, log)
	server.Start()
	t.Cleanup(server.Close)
	for _, stream := range []bool{false, true} {
		req := request(t, t.Context(), server, "/v1/chat/completions", fixtureAPIKey, "", map[string]any{
			"model": modelID, "stream": stream, "messages": []map[string]string{{"role": "user", "content": "secret\x00 fixture"}},
		})
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rec := httptest.NewRecorder()
			server.Config.Handler.ServeHTTP(rec, req)
			done <- rec
		}()
		claimed := waitForClaim(t, server)
		if claimed.Text != "secret fixture" {
			t.Fatalf("worker got %q", claimed.Text)
		}
		rec := call(t, server, "/worker/reply", fixtureWorkerKey, server.URL,
			map[string]string{"id": claimed.ID, "text": "private reply"})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("reply status %d", rec.Code)
		}
		reply := <-done
		if reply.Code != http.StatusOK || !strings.Contains(reply.Body.String(), `"estimated":true`) ||
			(stream && !strings.Contains(reply.Body.String(), `"choices":[]`)) {
			t.Fatalf("got %d %s", reply.Code, reply.Body.String())
		}
	}
	if strings.Count(logs.String(), `"nul_removed":1`) != 2 || !strings.Contains(logs.String(), `"status":200`) ||
		!strings.Contains(logs.String(), `"request_bytes":`) || strings.Contains(logs.String(), "secret") ||
		strings.Contains(logs.String(), "private reply") {
		t.Fatalf("unexpected log records: %s", logs.String())
	}
}
