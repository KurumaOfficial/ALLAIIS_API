// SPDX-License-Identifier: GPL-3.0-only

package vault

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	passphrase := []byte("test-vault-passphrase")
	if err := Init(t.Context(), path, passphrase); err != nil {
		t.Fatal(err)
	}
	store, err := Open(context.Background(), path, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

func TestResponseTurnsRoundTrip(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	if err := store.SaveResponse(ctx, "resp_1", []byte(`[{"role":"user","content":"secret prompt"}]`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, ok := store.LoadResponse(ctx, "resp_1")
	if !ok || !strings.Contains(string(raw), "secret prompt") {
		t.Fatalf("round trip failed: %s %v", raw, ok)
	}
	if _, ok := store.LoadResponse(ctx, "missing"); ok {
		t.Fatal("unknown id resolved")
	}
}

func TestResponseTurnsAreEncrypted(t *testing.T) {
	store, path := testStore(t)
	if err := store.SaveResponse(context.Background(), "resp_2", []byte(`PLAINTEXT_MARK`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PLAINTEXT_MARK") {
		t.Fatal("turn list is stored in clear text")
	}
}

func TestResponseTurnsExpire(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	if err := store.SaveResponse(ctx, "resp_3", []byte(`[]`), time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.LoadResponse(ctx, "resp_3"); ok {
		t.Fatal("expired entry resolved")
	}
}

func TestResponseTurnsPrune(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	for index := range 140 {
		if err := store.SaveResponse(ctx, fmt.Sprintf("resp_%03d", index), []byte(`[]`), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM response_turns`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count > 128 {
		t.Fatalf("kept %d entries", count)
	}
}
