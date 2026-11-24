// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

func TestStoredProxyTextWithoutAVault(t *testing.T) {
	if got := storedProxyText(context.Background(), nil); got != "" {
		t.Fatalf("nil store returned %q", got)
	}
	client := webprov.New(time.Second)
	applyProxiesToWeb(context.Background(), nil, client)
}

func TestPassphraseAvailable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ALLAIIS_VAULT_PASSPHRASE", "")
	if passphraseAvailable(dir) {
		t.Fatal("an empty directory reported a passphrase")
	}
	if err := os.WriteFile(filepath.Join(dir, passphraseFile), []byte("generated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !passphraseAvailable(dir) {
		t.Fatal("the generated passphrase was not found")
	}
	t.Setenv("ALLAIIS_VAULT_PASSPHRASE", "from-env")
	if !passphraseAvailable(dir) {
		t.Fatal("the environment value was not found")
	}
}
