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
// TestInitWithTheWrongPassphraseFails pins the other half of a repeated init:
// Init on an existing vault ignores the passphrase, so a wrong one used to look
// like success and every later command failed instead.
func TestInitWithTheWrongPassphraseFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	vaultPath := filepath.Join(dir, "vault.sqlite")
	right := []byte("local-dev-passphrase-right")
	if err := initialize(context.Background(), dir, configPath, vaultPath, right); err != nil {
		t.Fatal(err)
	}
	err := initialize(context.Background(), dir, configPath, vaultPath, []byte("local-dev-passphrase-wrong"))
	if err == nil || !strings.Contains(err.Error(), "does not open") {
		t.Fatalf("a wrong passphrase looked like success: %v", err)
	}
	store, openErr := vault.Open(context.Background(), vaultPath, right)
	if openErr != nil {
		t.Fatalf("the original passphrase stopped working: %v", openErr)
	}
	store.Close()
}

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
