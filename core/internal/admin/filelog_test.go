// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFileKeepsTheCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.log")
	writer, err := NewRotatingFile(path, 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		if _, err := writer.Write([]byte(strings.Repeat("x", 60) + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()

	for _, name := range []string{"gateway.log", "gateway.log.1"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Size() > 200 {
			t.Fatalf("%s grew to %d", name, info.Size())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "gateway.log.3")); !os.IsNotExist(err) {
		t.Fatal("kept more files than asked")
	}
}

func TestRotatingFileCreatesTheDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "gateway.log")
	writer, err := NewRotatingFile(path, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "hello") {
		t.Fatalf("%q %v", raw, err)
	}
}
