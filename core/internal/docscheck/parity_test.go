// SPDX-License-Identifier: GPL-3.0-only

// Package docscheck keeps the bilingual documents structurally in step. Content
// is written twice on purpose, so only the shape is compared: headings, tables,
// lists and code blocks.
package docscheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	testFunc = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
	testName = regexp.MustCompile("`(Test[A-Za-z0-9_]+)`")
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

// everyTest lists the test functions that exist in the tree.
func everyTest(t *testing.T) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	root := filepath.Join("..", "..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range testFunc.FindAllStringSubmatch(string(raw), -1) {
			found[match[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestCitedTestsExist(t *testing.T) {
	existing := everyTest(t)
	root := filepath.Join("..", "..", "..")
	for _, pair := range pairs {
		for _, path := range pair {
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			for _, match := range testName.FindAllStringSubmatch(string(raw), -1) {
				if !existing[match[1]] {
					t.Fatalf("%s cites %s, which no test defines", path, match[1])
				}
			}
		}
	}
}

func TestPairedDocsCiteTheSameTests(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, pair := range pairs {
		counts := make([]map[string]int, 2)
		for index, path := range pair {
			raw, err := os.ReadFile(filepath.Join(root, path))
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			counts[index] = map[string]int{}
			for _, match := range testName.FindAllStringSubmatch(string(raw), -1) {
				counts[index][match[1]]++
			}
		}
		for name, count := range counts[0] {
			if counts[1][name] != count {
				t.Fatalf("%s cites %s %d time(s), %s cites it %d", pair[0], name, count, pair[1], counts[1][name])
			}
		}
		for name, count := range counts[1] {
			if counts[0][name] != count {
				t.Fatalf("%s cites %s %d time(s), %s cites it %d", pair[1], name, count, pair[0], counts[0][name])
			}
		}
	}
}

// linkTarget finds relative link targets in markdown.
var linkTarget = regexp.MustCompile(`\]\(([^)#\s]+)`)

func TestLinksResolve(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var docs []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			relative, _ := filepath.Rel(root, path)
			docs = append(docs, relative)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range docs {
		raw, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, match := range linkTarget.FindAllStringSubmatch(string(raw), -1) {
			target := match[1]
			if strings.HasPrefix(target, "http") || strings.HasPrefix(target, "mailto:") ||
				strings.HasPrefix(target, "../../") {
				continue
			}
			resolved := filepath.Join(filepath.Dir(filepath.Join(root, path)), target)
			if _, err := os.Stat(resolved); err != nil {
				t.Fatalf("%s links %s, which does not exist", path, target)
			}
		}
	}
}
