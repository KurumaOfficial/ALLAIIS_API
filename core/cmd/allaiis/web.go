// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

const webUsage = `Usage: allaiis web <command>

  chrome            Open the capture Chrome profile on a DevTools port
  login             Capture a logged-in web session into the vault
  quota             Show the remaining web quota for a stored session
  e2ee              Verify a model enclave and send one sealed canary turn

web login flags: --provider venice-web --label <label> [--cookie <header>]
web e2ee flags: --provider venice-web --label <label> --model <e2ee model>
`

func runWeb(ctx context.Context, dataDir, configPath, vaultPath string, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, webUsage)
		return nil
	}
	command := args[0]
	flags := flag.NewFlagSet("web "+command, flag.ContinueOnError)
	var providerID, label, cookie, url, chrome, e2eeModel string
	switch command {
	case "chrome":
		flags.StringVar(&providerID, "provider", "venice-web", "web provider ID")
		flags.StringVar(&url, "url", "https://venice.ai/chat", "page to open")
		flags.StringVar(&chrome, "chrome", "", "Chrome executable (auto-detected by default)")
	case "login":
		flags.StringVar(&providerID, "provider", "venice-web", "web provider ID")
		flags.StringVar(&label, "label", "", "credential label within the pool")
		flags.StringVar(&cookie, "cookie", "", "paste a Cookie header instead of using Chrome")
	case "quota":
		flags.StringVar(&providerID, "provider", "venice-web", "web provider ID")
		flags.StringVar(&label, "label", "", "credential label within the pool")
	case "e2ee":
		flags.StringVar(&providerID, "provider", "venice-web", "web provider ID")
		flags.StringVar(&label, "label", "", "credential label within the pool")
		flags.StringVar(&e2eeModel, "model", "", "upstream e2ee model ID")
	default:
		return fmt.Errorf("unknown web command %q", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	provider, ok := cfg.Provider(providerID)
	if !ok || !provider.Web() || provider.Transport != config.TransportVeniceWeb {
		return fmt.Errorf("provider %q is not a venice_web provider", providerID)
	}
	if command == "chrome" {
		if chrome != "" {
			webprov.SetChromePath(chrome)
		}
		if err := webprov.LaunchChrome(webprov.DefaultProfileDir(dataDir), url); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Chrome started with profile %s\n", webprov.DefaultProfileDir(dataDir))
		fmt.Fprintln(os.Stdout, "Sign in to Venice, then run: allaiis web login --label <label>")
		return nil
	}
	passphrase, err := readPassphrase()
	if err != nil {
		return err
	}
	defer clear(passphrase)
	store, err := vault.Open(ctx, vaultPath, passphrase)
	if err != nil {
		return fmt.Errorf("open vault (run allaiis init for a new vault): %w", err)
	}
	defer store.Close()
	if command == "e2ee" {
		if e2eeModel == "" || label == "" {
			return errors.New("--model and --label are required")
		}
		return webE2EE(ctx, store, provider, providerID, e2eeModel, label)
	}
	if command == "quota" {
		floor := config.DefaultQuotaFloor
		if cfg, err := config.Load(configPath); err == nil {
			if cfg.QuotaFloor == 0 {
				floor = config.DefaultQuotaFloor
			} else if cfg.QuotaFloor < 0 {
				floor = 0
			} else {
				floor = cfg.QuotaFloor
			}
		}
		return webQuota(ctx, store, provider, providerID, label, floor)
	}
	if label == "" {
		return errors.New("--label is required")
	}
	var cred webprov.Credential
	if cookie != "" {
		cred, err = webprov.CaptureFromHeader(cookie)
	} else {
		cred, err = webprov.Capture(ctx)
	}
	if err != nil {
		return err
	}
	if cred.DistinctID == "" {
		cred.DistinctID = webprov.NewDistinctID()
	}
	client := webprov.New(60 * time.Second)
	limits, err := client.Probe(ctx, cred)
	if err != nil {
		return fmt.Errorf("validate the captured session: %w", err)
	}
	if client.Principal(ctx, cred) == "anonymous" {
		fmt.Fprintln(os.Stdout, "Captured a guest session: only the small free quota applies.")
		fmt.Fprintln(os.Stdout, "Sign in at https://venice.ai in the capture Chrome, then run this again for your plan quota.")
	} else {
		fmt.Fprintln(os.Stdout, "Captured a signed-in session.")
	}
	if _, err := store.Import(ctx, []vault.Entry{{
		Provider: providerID, Endpoint: provider.BaseURL, Label: label, Key: cred.Encode(),
	}}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Saved session %q. Restart a running gateway to reload its pool.\n", label)
	return writeJSON(os.Stdout, limits)
}

func webQuota(ctx context.Context, store *vault.Store, provider config.Provider, providerID, label string, floor int) error {
	entries, err := store.Entries(ctx, providerID, provider.BaseURL)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return errors.New("no stored web session; run allaiis web login")
	}
	client := webprov.New(60 * time.Second)
	results := make(map[string]any, len(entries))
	for _, entry := range entries {
		if label != "" && entry.Label != label {
			continue
		}
		cred, err := webprov.ParseCredential(entry.Key)
		if err != nil {
			results[entry.Label] = err.Error()
			continue
		}
		limits, err := client.Probe(ctx, cred)
		if err != nil {
			results[entry.Label] = err.Error()
			continue
		}
		row := map[string]any{"principal": client.Principal(ctx, cred), "limits": limits}
		// The same floor the panel uses, so a scripted check sees it too.
		if chat, ok := limits["chat"].(map[string]any); ok && floor > 0 {
			if remaining, ok := chat["remaining"].(float64); ok && remaining <= float64(floor) {
				row["low"] = true
			}
		}
		results[entry.Label] = row
	}
	if len(results) == 0 {
		return errors.New("label was not found for this provider")
	}
	return writeJSON(os.Stdout, results)
}

// webE2EE verifies the enclave and sends one sealed canary, which is the whole
// thing that can be checked without knowing whether a reply opens.
func webE2EE(ctx context.Context, store *vault.Store, provider config.Provider, providerID, model, label string) error {
	entries, err := store.Entries(ctx, providerID, provider.BaseURL)
	if err != nil {
		return err
	}
	var entry vault.Entry
	found := false
	for _, candidate := range entries {
		if candidate.Label == label {
			entry, found = candidate, true
			break
		}
	}
	if !found {
		return errors.New("label was not found for this provider")
	}
	cred, err := webprov.ParseCredential(entry.Key)
	if err != nil {
		return err
	}
	client := webprov.New(3 * time.Minute)
	probe, err := client.ProbeE2EE(ctx, cred, model)
	if err != nil {
		return err
	}
	var reply strings.Builder
	callErr := client.Chat(ctx, cred, webprov.Request{
		Model: model, E2EE: true, MaxTokens: 24,
		Messages: []webprov.Message{{Role: "user", Content: "Say hi in three words."}},
	}, func(event webprov.Event) error {
		if event.Kind == "content" {
			reply.WriteString(event.Content)
		}
		return nil
	})
	result := map[string]any{"attestation": probe, "reply": reply.String(), "decrypted": reply.Len() > 0}
	if callErr != nil {
		result["error"] = callErr.Error()
	}
	return writeJSON(os.Stdout, result)
}
