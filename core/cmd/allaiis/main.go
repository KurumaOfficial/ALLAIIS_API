// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/importer"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

const usage = `Usage: allaiis <command>

  init              Initialize the local configuration and encrypted vault
  import            Import API keys from environment variables or OpenCode
  account add       Add or replace a named API key
  account remove    Remove a key from the vault
  models discover   List models from the Venice or OpenCode public catalog
  models add        Register a provider-specific model alias
  status            Show configured models, key labels, and provider cooldowns
  key               Print the local gateway bearer key
  export opencode   Print a custom provider config for OpenCode
  up                Start the loopback gateway
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "allaiis:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		fmt.Fprint(os.Stdout, usage)
		return nil
	}
	dir := os.Getenv("ALLAIIS_DATA_DIR")
	if dir == "" {
		userConfig, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(userConfig, "allaiis")
	}
	configPath := filepath.Join(dir, "config.json")
	vaultPath := filepath.Join(dir, "vault.sqlite")
	if args[0] == "models" {
		return runModels(ctx, configPath, args[1:])
	}

	command := args[0]
	if command == "account" || command == "export" {
		if len(args) < 2 {
			return fmt.Errorf("run allaiis help for %s subcommands", command)
		}
		command += " " + args[1]
		args = args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var sources []string
	var providerID, label, keyEnv, authPath string
	switch command {
	case "import":
		flags.Func("from", "key source, env or opencode (repeatable)", func(source string) error {
			if source != "env" && source != "opencode" {
				return errors.New("expected env or opencode")
			}
			if !slices.Contains(sources, source) {
				sources = append(sources, source)
			}
			return nil
		})
		flags.StringVar(&providerID, "provider", "", "select a provider (defaults to providers in config)")
		flags.StringVar(&authPath, "path", "", "path to the OpenCode auth.json file")
		flags.StringVar(&label, "label", "", "override the label when importing one source")
	case "account add":
		flags.StringVar(&keyEnv, "key-env", "", "environment variable containing the API key (otherwise prompt)")
		fallthrough
	case "account remove":
		flags.StringVar(&providerID, "provider", "", "configured provider ID")
		flags.StringVar(&label, "label", "", "credential label within the pool")
	case "init", "key", "status", "export opencode", "up":
	default:
		return fmt.Errorf("unknown command %q. Run allaiis help", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "import" && (len(sources) == 0 || (authPath != "" && !slices.Contains(sources, "opencode")) ||
		(label != "" && len(sources) != 1)) {
		return errors.New("specify --from env or --from opencode. Use --path with opencode and --label with a single source")
	}
	if (command == "account add" || command == "account remove") && (providerID == "" || label == "") {
		return errors.New("--provider and --label are required")
	}
	passphrase, err := readPassphrase()
	if err != nil {
		return err
	}
	defer clear(passphrase)
	if command == "init" {
		if err := initialize(ctx, dir, configPath, vaultPath, passphrase); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "Configuration:", configPath)
		return nil
	}
	store, err := vault.Open(ctx, vaultPath, passphrase)
	if err != nil {
		return fmt.Errorf("open vault (run allaiis init for a new vault): %w", err)
	}
	defer store.Close()
	clear(passphrase)
	if command == "key" {
		_, err := fmt.Fprintln(os.Stdout, store.ClientKey())
		return err
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	switch command {
	case "import":
		entries, err := readImport(cfg, providerID, authPath, label, sources)
		if err != nil {
			return err
		}
		return saveEntries(ctx, store, entries)
	case "account add", "account remove":
		provider, ok := cfg.Provider(providerID)
		if !ok {
			return fmt.Errorf("unknown provider %q", providerID)
		}
		if command == "account add" {
			key, err := readAPIKey(keyEnv)
			if err != nil {
				return err
			}
			defer clear(key)
			return saveEntries(ctx, store, []vault.Entry{
				{Provider: providerID, Endpoint: provider.BaseURL, Label: label, Key: string(key)},
			})
		}
		removed, err := store.Remove(ctx, providerID, provider.BaseURL, label)
		if err != nil {
			return err
		}
		if !removed {
			return errors.New("credential label was not found for this provider")
		}
		fmt.Fprintln(os.Stdout, "Removed credential. Restart a running gateway to reload its key pool.")
		return nil
	}

	routes, err := router.New(ctx, cfg, store)
	if err != nil {
		return err
	}
	switch command {
	case "status":
		providers, err := routes.Status(ctx)
		if err != nil {
			return err
		}
		return writeJSON(os.Stdout, map[string]any{
			"listen": cfg.Listen, "min_privacy": cfg.MinPrivacy, "providers": providers, "models": routes.Models(cfg.MinPrivacy),
		})
	case "export opencode":
		return exportOpenCode(os.Stdout, cfg.Listen, routes.Models(cfg.MinPrivacy))
	case "up":
		return serve(ctx, cfg.Listen, gateway.New(routes, store.ClientKey(), cfg.MinPrivacy))
	}
	return errors.New("unhandled command")
}

func readPassphrase() ([]byte, error) {
	if value := os.Getenv("ALLAIIS_VAULT_PASSPHRASE"); value != "" {
		return []byte(value), nil
	}
	return readHidden("Vault passphrase", "ALLAIIS_VAULT_PASSPHRASE is required in non-interactive mode")
}

func readAPIKey(keyEnv string) ([]byte, error) {
	if keyEnv == "" {
		return readHidden("Provider API key", "--key-env is required in non-interactive mode")
	}
	key := os.Getenv(keyEnv)
	if key == "" {
		return nil, fmt.Errorf("environment variable %q is empty", keyEnv)
	}
	return []byte(key), nil
}

func readHidden(prompt, nonTTYError string) ([]byte, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, errors.New(nonTTYError)
	}
	fmt.Fprintf(os.Stderr, "%s: ", prompt)
	value, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	return value, err
}

func initialize(ctx context.Context, dir, configPath, vaultPath string, passphrase []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := vault.Init(ctx, vaultPath, passphrase); err != nil {
		return err
	}
	file, err := os.OpenFile(configPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	err = errors.Join(writeJSON(file, config.Default()), file.Close())
	if err != nil {
		return errors.Join(err, os.Remove(configPath))
	}
	return nil
}

func readImport(cfg config.Config, providerID, authPath, label string, sources []string) ([]vault.Entry, error) {
	ids, err := importer.Providers(cfg, providerID)
	if err != nil {
		return nil, err
	}
	var entries []vault.Entry
	for _, source := range sources {
		switch source {
		case "env":
			entries = append(entries, importer.Env(cfg, ids)...)
		case "opencode":
			imported, err := importer.OpenCode(cfg, ids, authPath)
			if err != nil {
				return nil, err
			}
			entries = append(entries, imported...)
		}
	}
	if label != "" {
		for i := range entries {
			entries[i].Label = label
		}
	}
	return entries, nil
}

func saveEntries(ctx context.Context, store *vault.Store, entries []vault.Entry) error {
	if len(entries) == 0 {
		return errors.New("no API keys matched the selected source and providers")
	}
	count, err := store.Import(ctx, entries)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Saved %d API key(s). Restart a running gateway to reload its key pool.\n", count)
	return nil
}

func exportOpenCode(w io.Writer, listen string, models []router.Model) error {
	if len(models) == 0 {
		return errors.New("configure a model and import a provider key before exporting")
	}
	options := make(map[string]any, len(models))
	for _, model := range models {
		options[model.ID] = map[string]string{"name": model.ID}
	}
	return writeJSON(w, map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"providers": map[string]any{"allaiis": map[string]any{
			"name": "ALLAIIS", "package": "@opencode/ai/providers/openai-compatible",
			"env": []string{"ALLAIIS_API_KEY"},
			"settings": map[string]string{
				"baseURL": "http://" + listen + "/v1",
				"apiKey":  "{env:ALLAIIS_API_KEY}",
			},
			"models": options,
		}},
	})
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func serve(ctx context.Context, address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	slog.Info("ALLAIIS gateway listening", "address", address)
	stopped := make(chan error, 1)
	go func() { stopped <- server.Serve(listener) }()
	select {
	case err := <-stopped:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
			return err
		}
		return nil
	}
}
