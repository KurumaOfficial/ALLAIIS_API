// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackupHelpDoesNotWrite pins the typo that made `backup --help` create a
// directory named `--help` instead of printing usage.
func TestBackupHelpDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	out := capture(t, func() {
		if err := runBackup(t.Context(), dir, []string{"--help"}); err != nil {
			t.Fatalf("help failed: %v", err)
		}
	})
	if !strings.Contains(out, "allaiis backup") {
		t.Fatalf("no usage: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "--help")); err == nil {
		t.Fatal("a directory named --help was created")
	}
	for _, args := range [][]string{{"-h"}, {"help"}, {"restore", "--help"}} {
		if err := runBackup(t.Context(), dir, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestBackupRejectsUnknownOptions(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"--out", "x"}, {"a", "b"}, {"restore", "x", "--no"}} {
		if err := runBackup(t.Context(), dir, args); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"listen":"127.0.0.1:8420","models":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vault.sqlite"), []byte("vault"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "saved")

	if err := runBackup(t.Context(), dir, []string{target}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, "vault.sqlite")); err != nil {
		t.Fatalf("vault not saved: %v", err)
	}

	// Without --force an existing file stops the restore.
	if err := runBackup(t.Context(), dir, []string{"restore", target}); err == nil {
		t.Fatal("overwrite without --force")
	}
	if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(t.Context(), dir, []string{"restore", target, "--force"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil || !strings.Contains(string(raw), "8420") {
		t.Fatalf("config not restored: %q %v", raw, err)
	}
}

func TestRestoreRejectsATamperedFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "saved")
	if err := runBackup(t.Context(), dir, []string{target}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.json"), []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := runBackup(t.Context(), dir, []string{"restore", target, "--force"}); err == nil {
		t.Fatal("tampered file accepted")
	}
}
