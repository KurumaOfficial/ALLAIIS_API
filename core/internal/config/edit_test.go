// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

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
