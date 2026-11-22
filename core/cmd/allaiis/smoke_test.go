// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

func smokeFixture(t *testing.T, fail bool) (dir, configPath, vaultPath string) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":{"message":"boom"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ready"}}]}`)
	}))
	t.Cleanup(upstream.Close)

	dir = t.TempDir()
	configPath = filepath.Join(dir, "config.json")
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"fixture": {BaseURL: upstream.URL}}
	cfg.Models = []config.Route{{ID: "one", Provider: "fixture", UpstreamModel: "fixture-model"}}
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	vaultPath = filepath.Join(dir, "vault.sqlite")
	pass := []byte("smoke-test-passphrase")
	if err := vault.Init(t.Context(), vaultPath, pass); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), vaultPath, pass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Import(t.Context(), []vault.Entry{{
		Provider: "fixture", Endpoint: upstream.URL, Label: "work", Key: "sk-fixture",
	}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	t.Setenv("ALLAIIS_VAULT_PASSPHRASE", string(pass))
	return dir, configPath, vaultPath
}

func TestSmokePassesEveryAlias(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, false)
	capture(t, func() {
		if err := runSmoke(t.Context(), dir, configPath, vaultPath, "", 2, 20*time.Second, false); err != nil {
			t.Fatalf("smoke failed: %v", err)
		}
	})
}

func TestSmokeReportsFailures(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, true)
	capture(t, func() {
		err := runSmoke(t.Context(), dir, configPath, vaultPath, "one", 1, 20*time.Second, false)
		if err == nil {
			t.Fatal("failing upstream reported success")
		}
	})
}

func TestSmokeRejectsUnknownAlias(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, false)
	capture(t, func() {
		if err := runSmoke(t.Context(), dir, configPath, vaultPath, "missing", 1, 20*time.Second, false); err == nil {
			t.Fatal("unknown alias accepted")
		}
	})
}

// capture redirects stdout for the duration of fn.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(reader)
		done <- string(body)
	}()
	defer func() { os.Stdout = original }()
	fn()
	writer.Close()
	os.Stdout = original
	return <-done
}
