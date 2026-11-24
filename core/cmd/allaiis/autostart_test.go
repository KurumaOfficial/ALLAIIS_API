// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestAutoPassphraseStartsAFreshDirectory pins the double-click path: no
// environment, no vault, no prompt; the next run uses the same generated key.
func TestAutoPassphraseStartsAFreshDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALLAIIS_VAULT_PASSPHRASE", "")
	configPath := filepath.Join(dir, "config.json")
	vaultPath := filepath.Join(dir, "vault.sqlite")

	first, err := autoPassphrase(t.Context(), dir, configPath, vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "vault.sqlite", "passphrase"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	second, err := autoPassphrase(t.Context(), dir, configPath, vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("a second start generated a different key")
	}
	read, err := readPassphrase(dir)
	if err != nil || !bytes.Equal(read, first) {
		t.Fatalf("readPassphrase: %v", err)
	}
}
