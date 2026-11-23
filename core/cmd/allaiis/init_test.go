// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

// TestInitKeepsWhatIsAlreadyThere pins the safety of a repeated first run: init
// must not overwrite a config or a vault that already exists.
func TestInitKeepsWhatIsAlreadyThere(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	vaultPath := filepath.Join(dir, "vault.sqlite")
	passphrase := []byte("local-dev-passphrase-init")

	existing := `{"listen":"127.0.0.1:9000","models":[{"id":"kept","provider":"p","upstream_model":"m"}]}`
	if err := os.WriteFile(configPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := initialize(context.Background(), dir, configPath, vaultPath, passphrase); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"id":"kept"`) {
		t.Fatalf("init overwrote the config: %s", raw)
	}

	store, err := vault.Open(context.Background(), vaultPath, passphrase)
	if err != nil {
		t.Fatalf("vault after init: %v", err)
	}
	key := store.ClientKey()
	store.Close()

	// A second init must not rotate the key or fail on the existing files.
	if err := initialize(context.Background(), dir, configPath, vaultPath, passphrase); err != nil {
		t.Fatalf("second init: %v", err)
	}
	again, err := vault.Open(context.Background(), vaultPath, passphrase)
	if err != nil {
		t.Fatalf("vault after the second init: %v", err)
	}
	defer again.Close()
	if again.ClientKey() != key {
		t.Fatal("a repeated init rotated the client key")
	}
}
