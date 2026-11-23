// SPDX-License-Identifier: GPL-3.0-only

package docscheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoControlCharacters guards the commands readers copy: an escape sequence
// that turned into a control byte once made `.\allaiis.exe` read as a bell
// character in two runbooks, and nothing noticed.
func TestNoControlCharacters(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	extensions := map[string]bool{
		".md": true, ".go": true, ".js": true, ".css": true, ".html": true,
		".yml": true, ".yaml": true, ".cmd": true, ".json": true,
	}
	checked := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if name := entry.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !extensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for number, line := range strings.Split(string(raw), "\n") {
			for _, char := range line {
				if char < 0x20 && char != '\t' && char != '\r' {
					rel, _ := filepath.Rel(root, path)
					t.Fatalf("%s:%d holds control character %#x: %q", rel, number+1, char, line)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Fatalf("only %d files scanned", checked)
	}
}
