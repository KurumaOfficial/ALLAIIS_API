// SPDX-License-Identifier: GPL-3.0-only

// Package docscheck keeps the bilingual documents structurally in step. Content
// is written twice on purpose, so only the shape is compared: headings, tables,
// lists and code blocks.
package docscheck

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pairs are the documents that exist twice, English and Russian.
var pairs = [][2]string{
	{"README.md", "README.ru.md"},
	{"docs/gui.md", "docs/gui.ru.md"},
	{"docs/quality.md", "docs/quality.ru.md"},
}

type shape struct {
	headings int
	// tables holds the length of every table in order, so one table growing while
	// another shrinks cannot balance out.
	tables  []int
	bullets int
	fences  int
}

// measure counts the structural lines of one document.
func measure(t *testing.T, path string) shape {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", path))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var got shape
	run := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "|") {
			run++
			continue
		}
		if run > 0 {
			got.tables = append(got.tables, run)
			run = 0
		}
		switch {
		case strings.HasPrefix(line, "#"):
			got.headings++
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "  - "):
			got.bullets++
		case strings.HasPrefix(line, "```"):
			got.fences++
		}
	}
	if run > 0 {
		got.tables = append(got.tables, run)
	}
	return got
}

func TestPairsExist(t *testing.T) {
	for _, pair := range pairs {
		for _, path := range pair {
			if _, err := os.Stat(filepath.Join("..", "..", "..", path)); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
	}
}

func TestPairedDocsKeepTheirShape(t *testing.T) {
	for _, pair := range pairs {
		english, russian := measure(t, pair[0]), measure(t, pair[1])
		if !slices.Equal(english.tables, russian.tables) || english.headings != russian.headings ||
			english.bullets != russian.bullets || english.fences != russian.fences {
			t.Fatalf("%s has shape %+v, %s has %+v", pair[0], english, pair[1], russian)
		}
	}
}
