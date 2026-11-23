// SPDX-License-Identifier: GPL-3.0-only

package vault

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openStore(t *testing.T, path, passphrase string) *Store {
	t.Helper()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := Init(context.Background(), path, []byte(passphrase)); err != nil {
			t.Fatalf("init %s: %v", path, err)
		}
	}
	store, err := Open(context.Background(), path, []byte(passphrase))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestImportSurvivesAReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	store := openStore(t, path, "local-dev-passphrase-one")
	if _, err := store.Import(context.Background(), []Entry{{
		Provider: "p", Endpoint: "https://p.example/api", Label: "work", Key: "sk-secret",
	}}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	again := openStore(t, path, "local-dev-passphrase-one")
	entries, err := again.Entries(context.Background(), "p", "https://p.example/api")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Label != "work" || entries[0].Key != "sk-secret" {
		t.Fatalf("entries %+v", entries)
	}
}

func TestWrongPassphraseDoesNotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	if err := Init(context.Background(), path, []byte("local-dev-passphrase-right")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, []byte("local-dev-passphrase-wrong")); err == nil {
		t.Fatal("a wrong passphrase opened the vault")
	}
}

// TestCiphertextIsBoundToItsRow pins the associated data: moving a sealed key to
// another label must not open, or the AAD would be decorative.
func TestCiphertextIsBoundToItsRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	store := openStore(t, path, "local-dev-passphrase")
	if _, err := store.Import(context.Background(), []Entry{{
		Provider: "p", Endpoint: "https://p.example/api", Label: "work", Key: "sk-secret",
	}}); err != nil {
		t.Fatal(err)
	}
	store.Close()

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE credentials SET label = 'stolen'`); err != nil {
		t.Fatal(err)
	}

	again := openStore(t, path, "local-dev-passphrase")
	if entries, err := again.Entries(context.Background(), "p", "https://p.example/api"); err == nil {
		t.Fatalf("a row moved to another label opened: %+v", entries)
	} else if !strings.Contains(err.Error(), "could not decrypt") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRotateClientKeyPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	store := openStore(t, path, "local-dev-passphrase")
	before := store.ClientKey()
	rotated, err := store.RotateClientKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rotated == before || store.ClientKey() != rotated {
		t.Fatalf("rotation did not take: %q %q", before, rotated)
	}
	store.Close()

	again := openStore(t, path, "local-dev-passphrase")
	if again.ClientKey() != rotated {
		t.Fatalf("rotated key lost: %q", again.ClientKey())
	}
}

// TestClearCooldownPrefixStopsAtTheKey pins the NUL handling: SQLite string
// functions stop at an embedded NUL, so the match happens in Go.
func TestClearCooldownPrefixStopsAtTheKey(t *testing.T) {
	store := openStore(t, filepath.Join(t.TempDir(), "vault.sqlite"), "local-dev-passphrase")
	ctx := context.Background()
	until := time.Now().Add(time.Minute)
	for _, key := range []string{"pro", "pro\x00glm", "prod", "guest"} {
		if err := store.SetCooldown(ctx, "p", key, until); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClearCooldownPrefix(ctx, "p", "pro"); err != nil {
		t.Fatal(err)
	}
	all, err := store.Cooldowns(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["pro"]; ok {
		t.Fatal("the label row survived")
	}
	if _, ok := all["pro\x00glm"]; ok {
		t.Fatal("the model row survived")
	}
	if _, ok := all["prod"]; !ok {
		t.Fatal("a longer name was cleared")
	}
	if _, ok := all["guest"]; !ok {
		t.Fatal("another label was cleared")
	}
}
