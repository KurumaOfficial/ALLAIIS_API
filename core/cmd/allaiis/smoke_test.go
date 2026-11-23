// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
			io.WriteString(w, `{"error":{"message":"boom","code":"boom"}}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		// The tool probe sends a tool list; answer it with a tool call.
		if bytes.Contains(body, []byte(`"tools"`)) {
			io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"list_files","arguments":"{\"path\":\".\"}"}}]}}]}`)
			return
		}
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
		if err := runSmoke(t.Context(), dir, configPath, vaultPath, "", 2, 20*time.Second, false, false, nil); err != nil {
			t.Fatalf("smoke failed: %v", err)
		}
	})
}

func TestSmokeReportsFailures(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, true)
	capture(t, func() {
		err := runSmoke(t.Context(), dir, configPath, vaultPath, "one", 1, 20*time.Second, false, false, nil)
		if err == nil {
			t.Fatal("failing upstream reported success")
		}
	})
}

func TestSmokeReportsAModelParkedForBalance(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, false)
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	// A pinned route, so the park belongs to a label the check can name.
	cfg.Models[0].CredentialLabel = "work"
	body, _ := json.Marshal(cfg)
	if err := os.WriteFile(configPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	provider, _ := cfg.Provider("fixture")
	store, err := vault.Open(t.Context(), vaultPath, []byte(os.Getenv("ALLAIIS_VAULT_PASSPHRASE")))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCooldown(t.Context(), "fixture", provider.BaseURL+"\x00work\x00one", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	store.Close()

	var errRun error
	out := capture(t, func() {
		errRun = runSmoke(t.Context(), dir, configPath, vaultPath, "one", 1, 20*time.Second, false, true, nil)
	})
	if errRun == nil {
		t.Fatal("a parked model reported success")
	}
	if !strings.Contains(out, "no balance until") {
		t.Fatalf("the park is not named: %s", out)
	}
}

func TestSmokeAllowsAnExpectedCode(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, true)
	var errRun error
	out := capture(t, func() {
		errRun = runSmoke(t.Context(), dir, configPath, vaultPath, "one", 1, 20*time.Second, false, true, []string{"upstream_error"})
	})
	t.Log(out)
	if errRun != nil {
		t.Fatalf("an expected code failed the run: %v", errRun)
	}
	if !strings.Contains(out, "1 expected") {
		t.Fatalf("allowance not reported: %s", out)
	}
}

func TestSmokeCode(t *testing.T) {
	if code := smokeCode([]byte(`{"error":{"code":"model_not_found"}}`)); code != "model_not_found" {
		t.Fatalf("code %q", code)
	}
	if code := smokeCode([]byte("not json")); code != "" {
		t.Fatalf("garbage gave %q", code)
	}
}

func TestSmokeRejectsUnknownAlias(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, false)
	capture(t, func() {
		if err := runSmoke(t.Context(), dir, configPath, vaultPath, "missing", 1, 20*time.Second, false, false, nil); err == nil {
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

func TestSmokeCarriesAToolProbe(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, false)
	out := capture(t, func() {
		if err := runSmoke(t.Context(), dir, configPath, vaultPath, "one", 1, 20*time.Second, true, false, nil); err != nil {
			t.Fatalf("smoke failed: %v", err)
		}
	})
	if !strings.Contains(out, "(tools)") || !strings.Contains(out, `"passed": 2`) {
		t.Fatalf("tool probe missing: %s", out)
	}
}

func TestSmokeToolProbeReportsAProseAnswer(t *testing.T) {
	_, _, _ = smokeFixture(t, false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"sure thing"}}]}`)
	}))
	t.Cleanup(upstream.Close)

	result := smokeTools(t.Context(), &http.Client{Timeout: 5 * time.Second}, upstream.URL, "sk", config.Route{ID: "one"})
	if result.OK || !strings.Contains(result.Detail, "without a tool call") {
		t.Fatalf("prose accepted: %+v", result)
	}
}

func TestSmokeToolCallReadsTheName(t *testing.T) {
	name, args := smokeToolCall([]byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"list_files","arguments":"{}"}}]}}]}`))
	if name != "list_files" || args != "{}" {
		t.Fatalf("got %q %q", name, args)
	}
	if name, _ := smokeToolCall([]byte(`{"choices":[{"message":{"content":"x"}}]}`)); name != "" {
		t.Fatalf("prose produced %q", name)
	}
}

func TestSmokeToolProbeNeedsTheArgument(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Right name, no argument: the client would reject it.
		io.WriteString(w, `{"choices":[{"message":{"tool_calls":[{"function":{"name":"list_files","arguments":"{}"}}]}}]}`)
	}))
	t.Cleanup(upstream.Close)

	result := smokeTools(t.Context(), &http.Client{Timeout: 5 * time.Second}, upstream.URL, "sk", config.Route{ID: "one"})
	if result.OK || !strings.Contains(result.Detail, "without path") {
		t.Fatalf("empty arguments accepted: %+v", result)
	}
}

// TestSmokeToolProbeSpeaksTheRouteShape pins the endpoint per protocol: a
// Messages alias asked through Chat Completions answered model_not_found, so the
// tool probe proved nothing for that route.
func TestSmokeToolProbeSpeaksTheRouteShape(t *testing.T) {
	cases := map[config.Protocol]struct {
		path string
		body string
	}{
		config.Messages: {
			path: "/v1/messages",
			body: `{"content":[{"type":"tool_use","name":"list_files","input":{"path":"."}}]}`,
		},
		config.Responses: {
			path: "/v1/responses",
			body: `{"output":[{"type":"function_call","name":"list_files","arguments":"{\"path\":\".\"}"}]}`,
		},
	}
	for protocol, want := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != want.path {
				t.Errorf("%s asked %s", protocol, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, want.body)
		}))
		result := smokeTools(t.Context(), &http.Client{Timeout: 5 * time.Second}, server.URL, "sk",
			config.Route{ID: "one", Protocol: protocol})
		server.Close()
		if !result.OK {
			t.Fatalf("%s: %+v", protocol, result)
		}
	}
}

func TestSmokeCanSkipTheToolProbe(t *testing.T) {
	dir, configPath, vaultPath := smokeFixture(t, false)
	out := capture(t, func() {
		if err := runSmoke(t.Context(), dir, configPath, vaultPath, "one", 1, 20*time.Second, true, true, nil); err != nil {
			t.Fatalf("smoke failed: %v", err)
		}
	})
	if strings.Contains(out, "(tools)") || !strings.Contains(out, `"passed": 1`) {
		t.Fatalf("probe not skipped: %s", out)
	}
}
