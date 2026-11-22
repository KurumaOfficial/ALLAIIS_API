// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const backupUsage = `allaiis backup — save the data directory for safekeeping

  allaiis backup <directory>     Copy the vault, the config and the tool record
  allaiis backup restore <directory> --force
                                 Copy them back over the data directory
`

// backupFiles are the files worth keeping. Logs and the record of unknown tool
// names are not secrets, but the record is small and keeps a warning honest.
var backupFiles = []string{"vault.sqlite", "config.json", "tool-names.json"}

type backupManifest struct {
	Created string            `json:"created"`
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

// runBackup saves or restores the data directory.
func runBackup(ctx context.Context, dir string, args []string) error {
	_ = ctx
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, backupUsage)
		return nil
	}
	if args[0] == "restore" {
		if len(args) < 2 {
			return fmt.Errorf("name a directory to restore from")
		}
		force := len(args) > 2 && args[2] == "--force"
		return restoreBackup(dir, args[1], force)
	}
	return writeBackup(dir, args[0])
}

// writeBackup copies the live files and records their hashes.
func writeBackup(dir, target string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	manifest := backupManifest{Created: time.Now().UTC().Format(time.RFC3339), Version: version, Files: map[string]string{}}
	for _, name := range backupFiles {
		source := filepath.Join(dir, name)
		raw, err := os.ReadFile(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		manifest.Files[name] = hex.EncodeToString(sum[:])
		if err := os.WriteFile(filepath.Join(target, name), raw, 0o600); err != nil {
			return err
		}
	}
	if len(manifest.Files) == 0 {
		return fmt.Errorf("nothing to back up in %s", dir)
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), encoded, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Saved %d file(s) to %s\n", len(manifest.Files), target)
	return nil
}

// restoreBackup copies files back, checking each against the manifest.
func restoreBackup(dir, source string, force bool) error {
	raw, err := os.ReadFile(filepath.Join(source, "manifest.json"))
	if err != nil {
		return fmt.Errorf("no manifest in %s: %w", source, err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("the manifest is unreadable: %w", err)
	}
	for name, want := range manifest.Files {
		incoming, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(incoming)
		if got := hex.EncodeToString(sum[:]); got != want {
			return fmt.Errorf("%s does not match its manifest hash", name)
		}
		target := filepath.Join(dir, name)
		if !force {
			if _, err := os.Stat(target); err == nil {
				return fmt.Errorf("%s exists; pass --force to overwrite", target)
			}
		}
		if err := os.WriteFile(target, incoming, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Restored %s\n", name)
	}
	fmt.Fprintln(os.Stdout, "Stop the gateway before restoring, then start it again.")
	return nil
}

// copyFile is used by tests and keeps the copy small.
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
