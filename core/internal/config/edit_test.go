// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAddModelValidatesProtocolAndCredentialScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"models":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		alias, provider, label string
		protocol               Protocol
		errorText              string
	}{
		{"claude-native", "anthropic", "", Messages, ""},
		{"openai-response", "openai", "work", Responses, ""},
		{"unbound", "openai", "", Responses, "require a credential_label"},
		{"wrong-provider", "openai", "", Messages, "has no messages endpoint"},
		{"unknown-protocol", "openai", "", "other", "protocol must be"},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			route := Route{ID: tc.alias, Provider: tc.provider, Protocol: tc.protocol,
				UpstreamModel: "upstream", CredentialLabel: tc.label}
			added, err := AddModel(t.Context(), path, route)
			if tc.errorText != "" {
				if added || err == nil || !strings.Contains(err.Error(), tc.errorText) {
					t.Fatalf("unexpected result: added=%t, err=%v", added, err)
				}
				return
			}
			if !added || err != nil {
				t.Fatalf("add native route: added=%t, err=%v", added, err)
			}
		})
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 2 || cfg.Models[0].Protocol != Messages ||
		cfg.Models[1].Protocol != Responses || cfg.Models[1].CredentialLabel != "work" {
		t.Fatalf("unexpected native routes after reload: %+v", cfg.Models)
	}
}

func TestAddModelPreservesConcurrentEditsAndRejectsRemapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := `{"listen":"127.0.0.1:8172","models":[{"id":"original","provider":"deepseek","upstream_model":"deepseek-flash"}]}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	routes := []Route{
		{ID: "DeepSeek-v4.1-flash-venice", Provider: "venice", UpstreamModel: "deepseek-v4-1-flash-1017"},
		{ID: "DeepSeek-v4.1-flash-opencode-go", Provider: "opencode-go", UpstreamModel: "deepseek-v4.1-flash", CredentialLabel: "workspace"},
	}
	var edits sync.WaitGroup
	for _, route := range routes {
		edits.Go(func() {
			if added, err := AddModel(t.Context(), path, route); err != nil || !added {
				t.Errorf("add %q: added=%t err=%v", route.ID, added, err)
			}
		})
	}
	edits.Wait()

	if added, err := AddModel(t.Context(), path, routes[1]); err != nil || added {
		t.Fatalf("repeat add should be a no-op: added=%t err=%v", added, err)
	}
	remapped := routes[0]
	remapped.Provider = "opencode"
	if _, err := AddModel(t.Context(), path, remapped); err == nil {
		t.Fatal("conflicting alias should be rejected")
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:8172" || len(cfg.Models) != 3 || cfg.Models[0].ID != "original" {
		t.Fatalf("model edits lost previous settings: %+v", cfg)
	}
	models := make(map[string]Route)
	for _, model := range cfg.Models {
		models[model.ID] = model
	}
	for _, route := range routes {
		route.Privacy = Consumer
		if models[route.ID] != route {
			t.Errorf("incorrect stored route: %+v", models[route.ID])
		}
	}
}

func TestAddModelWithTemporaryWindowsReader(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows file sharing behavior")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"models":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	time.AfterFunc(75*time.Millisecond, func() { closed <- reader.Close() })
	added, addErr := AddModel(t.Context(), path, Route{ID: "demo", Provider: "openai", UpstreamModel: "upstream"})
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if addErr != nil || !added {
		t.Fatalf("add model after temporary reader closed: added=%t, err=%v", added, addErr)
	}
}

func TestNewPresetsPreservePreviouslyConfiguredProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
		"providers": {
			"anthropic": {"base_url": "http://127.0.0.1:18202/v1"},
			"arena": {"base_url": "https://api.arena.ai/v1"}
		},
		"models": []
	}`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	custom, found := cfg.Provider("anthropic")
	if !found || custom.CatalogURL() != "http://127.0.0.1:18202/v1/models" || custom.CatalogFormat != OpenAICatalog {
		t.Fatalf("existing provider was changed by a new preset: %+v", custom)
	}
	count := 0
	for _, id := range cfg.ProviderIDs() {
		if id == "arena-ai" {
			t.Fatal("preset duplicated the base URL of the existing arena provider")
		}
		if id == "anthropic" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("custom provider and preset appeared %d times", count)
	}
}

func TestSetModelDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"models":[{"id":"a","provider":"openai","upstream_model":"m"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetModelDisabled(t.Context(), path, "a", true); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Models[0].Disabled {
		t.Fatal("alias not muted")
	}
	if err := SetModelDisabled(t.Context(), path, "missing", true); err == nil {
		t.Fatal("unknown alias accepted")
	}
}

func TestCapabilityValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	route := `{"id":"a","provider":"openai","upstream_model":"m"`
	for _, entry := range []struct {
		extra string
		ok    bool
	}{
		{`,"reasoning":true,"max_context":128000,"max_response":8000,"max_images":4,"temperature":0.4,"top_p":0.8}`, true},
		{`,"max_context":-1}`, false},
		{`,"max_response":2000000}`, false},
		{`,"max_images":51}`, false},
		{`,"inactivity_ms":120000}`, true},
		{`,"inactivity_ms":100}`, false},
		{`,"temperature":2.5}`, false},
		{`,"top_p":1.2}`, false},
	} {
		body := `{"models":[` + route + entry.extra + `]}`
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if entry.ok && err != nil {
			t.Fatalf("%s: %v", entry.extra, err)
		}
		if !entry.ok && err == nil {
			t.Fatalf("%s accepted", entry.extra)
		}
	}
}

func TestDefaultAliasForWebChats(t *testing.T) {
	cases := []struct {
		provider, model string
		web             bool
		want            string
	}{
		{"venice-web", "deepseek-v4-1-flash-uncensored", true, "deepseek-v4-1-flash-uncensored-venice"},
		{"venice-web-eu", "deepseek-v4-1-flash-uncensored", true, "deepseek-v4-1-flash-uncensored-venice"},
		{"other-web", "model-one", true, "model-one-WebAPI"},
		{"openai", "gpt-4", false, "gpt-4-openai"},
	}
	for _, tc := range cases {
		if got := DefaultAliasFor(tc.provider, tc.model, tc.web); got != tc.want {
			t.Errorf("%s/%s: got %q want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}
