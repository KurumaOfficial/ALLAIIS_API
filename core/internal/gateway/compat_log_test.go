// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
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
		log.Info("completion", "origin", value, "model", fixtureModelID)
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
