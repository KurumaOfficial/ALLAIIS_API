// SPDX-License-Identifier: GPL-3.0-only

package router

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

func routerFixture(t *testing.T) (*Router, *vault.Store, config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	pass := []byte("router-test-passphrase")
	if err := vault.Init(t.Context(), path, pass); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), path, pass)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	endpoint := "http://127.0.0.1:9999"
	if _, err := store.Import(t.Context(), []vault.Entry{
		{Provider: "fixture", Endpoint: endpoint, Label: "one", Key: "key-one"},
		{Provider: "fixture", Endpoint: endpoint, Label: "two", Key: "key-two"},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Providers = map[string]config.Provider{"fixture": {BaseURL: endpoint}}
	cfg.Models = []config.Route{
		{ID: "pinned", Provider: "fixture", UpstreamModel: "m", CredentialLabel: "one", Privacy: config.Consumer},
		{ID: "pool", Provider: "fixture", UpstreamModel: "m", Privacy: config.Consumer},
	}
	routes, err := New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	return routes, store, cfg
}

func TestCooldownIsScopedToThePinnedLabel(t *testing.T) {
	routes, _, _ := routerFixture(t)
	target, _, err := routes.Resolve(t.Context(), "pinned", config.Chat, config.Consumer, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if err := routes.SetCooldown(t.Context(), target, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := routes.Resolve(t.Context(), "pinned", config.Chat, config.Consumer, [32]byte{}); !errors.Is(err, ErrCooldown) {
		t.Fatalf("pinned alias not cooling: %v", err)
	}
	if _, _, err := routes.Resolve(t.Context(), "pool", config.Chat, config.Consumer, [32]byte{}); err != nil {
		t.Fatalf("unpinned alias blocked by a pinned cooldown: %v", err)
	}
}

func TestCooldownOnUnpinnedPoolCoolsEveryLabel(t *testing.T) {
	routes, _, _ := routerFixture(t)
	target, _, err := routes.Resolve(t.Context(), "pool", config.Chat, config.Consumer, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if err := routes.SetCooldown(t.Context(), target, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := routes.Resolve(t.Context(), "pinned", config.Chat, config.Consumer, [32]byte{}); !errors.Is(err, ErrCooldown) {
		t.Fatalf("provider-wide cooldown ignored by a pinned alias: %v", err)
	}
}

func TestStatusReportsPerLabelCooldown(t *testing.T) {
	routes, _, _ := routerFixture(t)
	target, _, err := routes.Resolve(t.Context(), "pinned", config.Chat, config.Consumer, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if err := routes.SetCooldown(t.Context(), target, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	status, err := routes.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != 1 || status[0].Cooldowns["one"].IsZero() || !status[0].Cooldowns["two"].IsZero() {
		t.Fatalf("per-label cooldown missing: %+v", status)
	}
}

func TestDisabledAliasIsNotResolved(t *testing.T) {
	_, store, cfg := routerFixture(t)
	cfg.Models[0].Disabled = true
	routes, err := New(t.Context(), cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range routes.Models(config.Public) {
		if model.ID == cfg.Models[0].ID {
			t.Fatalf("disabled alias listed: %s", model.ID)
		}
	}
	if _, _, err := routes.Resolve(t.Context(), cfg.Models[0].ID, config.Chat, config.Consumer, [32]byte{}); !errors.Is(err, ErrModel) {
		t.Fatalf("disabled alias resolved: %v", err)
	}
}
