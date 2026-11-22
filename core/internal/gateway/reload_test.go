// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
)

func writeConfig(t *testing.T, path string, cfg config.Config) {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReloadSwapsAndKeepsPreviousOnError(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, cfg)
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithReload(path, store)
	if got := len(live.Models()); got != 3 {
		t.Fatalf("initial models %d", got)
	}

	cfg.Models = append(cfg.Models, config.Route{
		ID: "added", Provider: "fixture", UpstreamModel: "real-model", Privacy: config.Consumer,
	})
	writeConfig(t, path, cfg)
	if err := live.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := len(live.Models()); got != 4 {
		t.Fatalf("reload did not apply: %d", got)
	}

	if err := os.WriteFile(path, []byte(`{"models":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := live.Reload(t.Context()); err == nil {
		t.Fatal("broken config reloaded")
	}
	if got := len(live.Models()); got != 4 {
		t.Fatalf("failed reload dropped routes: %d", got)
	}
}

func TestReloadUpdatesPrivacyFloorAndKey(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, cfg)
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithReload(path, store)
	cfg.MinPrivacy = config.ZDR
	writeConfig(t, path, cfg)
	if err := live.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if live.Minimum() != config.ZDR {
		t.Fatalf("privacy floor not applied: %s", live.Minimum())
	}
	rotated, err := store.RotateClientKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	live.SetClientKey(rotated)
	if !live.ValidKey(rotated) {
		t.Fatal("new key not adopted")
	}
}

func TestWatchReloadsChangedConfig(t *testing.T) {
	store, cfg := setup(t, "http://127.0.0.1:1")
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfig(t, path, cfg)
	routes, err := router.New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithReload(path, store)
	watchCtx, stop := context.WithCancel(t.Context())
	defer stop()
	go live.Watch(watchCtx)
	time.Sleep(250 * time.Millisecond)

	cfg.Models = append(cfg.Models, config.Route{
		ID: "watched", Provider: "fixture", UpstreamModel: "real-model", Privacy: config.Consumer,
	})
	writeConfig(t, path, cfg)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(live.Models()) == 4 {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("watcher did not reload, models %d", len(live.Models()))
}
