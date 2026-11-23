// SPDX-License-Identifier: GPL-3.0-only

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadAcceptsABOM pins the file a Windows editor can produce: a byte-order
// mark is not part of the JSON, and refusing it made the gateway look broken
// with a syntax error pointing at the first brace.
func TestLoadAcceptsABOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := append([]byte{0xEF, 0xBB, 0xBF}, []byte(`{"listen":"127.0.0.1:8420","models":[]}`)...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("a BOM stopped the load: %v", err)
	}
	if cfg.Listen != "127.0.0.1:8420" {
		t.Fatalf("listen %q", cfg.Listen)
	}
}
