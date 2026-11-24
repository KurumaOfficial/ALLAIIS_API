// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

// TestCatalogNamesTheLabelsToPick pins the message a reader sees next: the
// provider is already selected, so the error has to name the labels.
func TestCatalogNamesTheLabelsToPick(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	passphrase := []byte("fixture-vault-passphrase")
	if err := vault.Init(t.Context(), path, passphrase); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), path, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	provider := config.Provider{BaseURL: "https://web.example/api"}
	if _, err := store.Import(t.Context(), []vault.Entry{
		{Provider: "web", Endpoint: provider.BaseURL, Label: "guest", Key: "a"},
		{Provider: "web", Endpoint: provider.BaseURL, Label: "pro", Key: "b"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := catalogCredential(t.Context(), store, "web", provider, ""); err == nil ||
		!strings.Contains(err.Error(), "guest, pro") {
		t.Fatalf("message %v", err)
	}
}

func TestCatalogUsesOnlyTheSelectedCredential(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer workspace-b-key" {
			t.Error("catalog reached the wrong endpoint or used the wrong credential")
		}
		fmt.Fprint(w, `{"data":[{"id":"workspace-b-model"}]}`)
	}))
	defer server.Close()

	provider := config.Provider{BaseURL: server.URL + "/v1"}
	path := filepath.Join(t.TempDir(), "vault.sqlite")
	passphrase := []byte("fixture-vault-passphrase")
	if err := vault.Init(t.Context(), path, passphrase); err != nil {
		t.Fatal(err)
	}
	store, err := vault.Open(t.Context(), path, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Import(t.Context(), []vault.Entry{
		{Provider: "custom", Endpoint: provider.BaseURL, Label: "a", Key: "workspace-a-key"},
		{Provider: "custom", Endpoint: provider.BaseURL, Label: "b", Key: "workspace-b-key"},
	}); err != nil {
		t.Fatal(err)
	}

	ambiguous := discoverProvider(t.Context(), store, "custom", provider, "", "")
	missing := discoverProvider(t.Context(), store, "custom", provider, "", "missing")
	if ambiguous.Status != "needs_label" || missing.Status != "needs_key" || requests.Load() != 0 {
		t.Fatal("catalog used a credential without an unambiguous selection")
	}
	result := discoverProvider(t.Context(), store, "custom", provider, "", "b")
	if result.Status != "ok" || result.Credential != "b" || result.Scope != "credential" ||
		len(result.Models) != 1 || result.Models[0].ID != "workspace-b-model" || requests.Load() != 1 {
		t.Fatalf("unexpected named credential result: %+v", result)
	}
}
