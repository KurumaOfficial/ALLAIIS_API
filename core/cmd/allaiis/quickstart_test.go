// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func TestQuickStartOffline(t *testing.T) {
	const upstreamKey = "quick-start-fixture-key"
	const responseJSON = `{"object":"chat.completion","model":"fixture-flash","choices":[{"message":{"role":"assistant","content":"hello"}}]}`
	const responseSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" ||
			r.Header.Get("Authorization") != "Bearer "+upstreamKey ||
			json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "fixture-flash" {
			t.Error("quick-start request reached the fixture with incorrect routing or credentials")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, responseSSE)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, responseJSON)
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	t.Setenv("ALLAIIS_DATA_DIR", dir)
	t.Setenv("ALLAIIS_VAULT_PASSPHRASE", "quick-start-test-passphrase")
	t.Setenv("ALLAIIS_QUICKSTART_FIXTURE_KEY", upstreamKey)
	t.Setenv("ALLAIIS_QUICKSTART_TEST_PROCESS", "1")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, binary, append([]string{"-test.run=^TestQuickStartProcess$", "--"}, args...)...)
	}
	runCLI := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		out, err := command(ctx, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}

	runCLI("init")
	configPath := filepath.Join(dir, "config.json")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	port, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = port.Addr().String()
	cfg.Providers = map[string]config.Provider{"fixture": {
		BaseURL: upstream.URL + "/v1", KeyEnv: "ALLAIIS_QUICKSTART_FIXTURE_KEY",
	}}
	if err := port.Close(); err != nil {
		t.Fatal(err)
	}
	configBody, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configBody, 0600); err != nil {
		t.Fatal(err)
	}

	runCLI("import", "--from", "env", "--provider", "fixture")
	runCLI("models", "add", "--provider", "fixture", "--upstream-model", "fixture-flash", "--name", "flash")
	key := runCLI("key")
	if !strings.HasPrefix(key, "sk-allaiis-") || strings.ContainsAny(key, " \r\n\t") {
		t.Fatal("key command did not print a single local API key")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	up := command(ctx, "up")
	if err := up.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		up.Wait()
	})

	client := &http.Client{Timeout: 500 * time.Millisecond}
	baseURL := "http://" + cfg.Listen
	deadline := time.Now().Add(10 * time.Second)
	for {
		health, err := client.Get(baseURL + "/healthz")
		if err == nil {
			health.Body.Close()
			if health.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("gateway did not become healthy")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, test := range []struct {
		stream       bool
		media, reply string
	}{
		{false, "application/json", responseJSON},
		{true, "text/event-stream", responseSSE},
	} {
		payload := fmt.Sprintf(`{"model":"flash-fixture","messages":[{"role":"user","content":"hello"}],"stream":%t}`, test.stream)
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/v1/chat/completions", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		reply, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		result, err := io.ReadAll(reply.Body)
		reply.Body.Close()
		if err != nil || reply.StatusCode != http.StatusOK || reply.Header.Get("Content-Type") != test.media ||
			string(result) != test.reply {
			t.Fatalf("quick-start %s request: HTTP %d, body %q, read error %v", test.media, reply.StatusCode, result, err)
		}
	}
}

func TestQuickStartProcess(t *testing.T) {
	if os.Getenv("ALLAIIS_QUICKSTART_TEST_PROCESS") != "1" {
		return
	}
	if separator := slices.Index(os.Args, "--"); separator >= 0 {
		os.Args = append([]string{"allaiis"}, os.Args[separator+1:]...)
		main()
		os.Exit(0)
	}
	t.Fatal("quick-start child did not receive CLI arguments")
}
