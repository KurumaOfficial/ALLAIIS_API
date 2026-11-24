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

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/admin"
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
  models discover   List upstream models from one or all provider catalogs
  models add        Register a model alias for chat, responses, or messages
  web chrome        Open the capture browser for a web provider
  web login         Capture a logged-in web session into the vault
  web quota         Show remaining quota for stored web sessions
  web e2ee          Attest a model enclave and send one sealed canary
  status            Show configured models, key labels, and provider cooldowns
  doctor            Check config, vault, listener, aliases and capture browser
  backup            Save or restore the vault and config
  update            Replace this binary with the latest release
  service           Register the gateway to start at logon
  tools             Inspect or clear the tool name record
  smoke             Probe aliases through an in-process gateway
  key               Print the local gateway bearer key
  version           Print the version (also -v or --version)
  export opencode   Print a custom provider config for OpenCode
  up                Start the loopback gateway
  gui               Start the gateway and open the control panel
`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "allaiis:", err)
		os.Exit(1)
	}
}

// flagOutput receives parse errors and usage; tests point it at io.Discard.
var flagOutput io.Writer = os.Stderr

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		fmt.Fprint(os.Stdout, usage)
		return nil
	}
	if slices.Contains([]string{"version", "-v", "--version"}, args[0]) {
		fmt.Fprintf(os.Stdout, "allaiis %s\n", version)
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
	if args[0] == "web" {
		return runWeb(ctx, dir, configPath, vaultPath, args[1:])
	}
	if args[0] == "update" {
		return runUpdate(ctx, args[1:])
	}
	if args[0] == "backup" {
		return runBackup(ctx, dir, args[1:])
	}
	if args[0] == "service" {
		return runService(ctx, dir, args[1:])
	}
	if args[0] == "tools" {
		origin := "the default config directory"
		if os.Getenv("ALLAIIS_DATA_DIR") != "" {
			origin = "ALLAIIS_DATA_DIR"
		}
		return runTools(dir, origin, configPath, args[1:])
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
	flags.SetOutput(flagOutput)
	var sources []string
	var providerID, label, keyEnv, authPath, listen, ui string
	var noOpen, asJSON bool
	var attempts int
	var timeout, smokeEvery time.Duration
	var smokeWebhook, logFile string
	var logMaxMB, logKeep int
	// Probing tools costs a request per sweep; an operator may switch it off.
	var noToolProbe bool
	// Codes that are expected on this host and should not fail the run.
	var allow []string
	// Offline skips the one network check doctor makes.
	var offline bool
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
	case "up", "gui":
		// Only the flags this command reads: the shared chain used to advertise
		// smoke and doctor options that were ignored here.
		flags.StringVar(&logFile, "log-file", "", "append the log here, rotated")
		flags.IntVar(&logMaxMB, "log-max-mb", 16, "rotate the log file at this size")
		flags.IntVar(&logKeep, "log-keep", 3, "how many rotated log files to keep")
		flags.StringVar(&listen, "listen", "", "gateway address (defaults to the configured one)")
		if command == "gui" {
			flags.StringVar(&ui, "ui", "127.0.0.1:0", "control panel address")
			flags.BoolVar(&noOpen, "no-open", false, "do not open a browser")
			flags.DurationVar(&smokeEvery, "smoke-every", 0, "probe every alias on this interval (e.g. 15m)")
			flags.StringVar(&smokeWebhook, "smoke-webhook", "", "POST failures and regressions to this URL")
			flags.BoolVar(&noToolProbe, "no-tools", false, "skip the tool parser probe")
		}
	case "smoke":
		flags.StringVar(&label, "alias", "", "probe only this alias")
		flags.IntVar(&attempts, "attempts", 1, "repeat every probe this many times")
		flags.DurationVar(&timeout, "timeout", 4*time.Minute, "per-request timeout")
		flags.BoolVar(&asJSON, "json", false, "print JSON instead of a table")
		flags.BoolVar(&noToolProbe, "no-tools", false, "skip the tool parser probe")
		flags.Func("allow", "error code or HTTP status that does not fail the run (repeatable)", func(code string) error {
			allow = append(allow, code)
			return nil
		})
	case "doctor":
		flags.BoolVar(&offline, "offline", false, "skip the network enclave attestation")
	case "init", "key", "status", "export opencode":
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
	closeLog, err := attachLogFile(logFile, logMaxMB, logKeep)
	if err != nil {
		return err
	}
	defer closeLog()
	if command == "gui" {
		return runGUI(ctx, dir, configPath, vaultPath, listen, ui, !noOpen, smokeEvery, smokeWebhook, noToolProbe)
	}
	if command == "smoke" {
		return runSmoke(ctx, dir, configPath, vaultPath, label, attempts, timeout, asJSON, noToolProbe, allow)
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
		fmt.Fprintln(os.Stdout, "Removed credential. A running gateway reloads it within a few seconds.")
		return nil
	}

	routes, err := router.New(ctx, cfg, store)
	if err != nil {
		return err
	}
	switch command {
	case "doctor":
		checks := admin.Diagnose(ctx, store, configPath)
		if offline {
			checks = admin.DiagnoseOffline(ctx, store, configPath)
		}
		if err := writeJSON(os.Stdout, map[string]any{"checks": checks}); err != nil {
			return err
		}
		for _, item := range checks {
			if item.Status == "error" {
				return errors.New("diagnostics found problems")
			}
		}
		return nil
	case "status":
		providers, err := routes.Status(ctx)
		if err != nil {
			return err
		}
		return writeJSON(os.Stdout, map[string]any{
			"version": version, "listen": cfg.Listen, "min_privacy": cfg.MinPrivacy,
			"providers": providers, "models": routes.Models(cfg.MinPrivacy),
		})
	case "export opencode":
		return exportOpenCode(os.Stdout, cfg.Listen, routes.Models(cfg.MinPrivacy))
	case "up":
		// The flag was registered and ignored: only gui applied it.
		if listen != "" {
			cfg.Listen = listen
		}
		if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
			return fmt.Errorf("invalid listen address: %w", err)
		}
		advertise(os.Stdout, cfg, routes.Models(cfg.MinPrivacy))
		live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithVersion(version).WithConfig(cfg).WithToolNameTTLFor(retentionTTL(cfg, configPath)).WithStateDir(ctx, dir).WithReload(configPath, store)
		go live.Watch(ctx)
		return serve(ctx, cfg.Listen, live)
	}
	return errors.New("unhandled command")
}

// attachLogFile adds a rotating file next to the terminal, for a gateway that
// runs for days.
func attachLogFile(path string, maxMB, keep int) (func(), error) {
	if path == "" {
		return func() {}, nil
	}
	writer, err := admin.NewRotatingFile(path, int64(maxMB)<<20, keep)
	if err != nil {
		return nil, err
	}
	logWriter = writer
	stderr := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	file := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(slog.NewMultiHandler(stderr, file)))
	return func() { writer.Close() }, nil
}

// logWriter is the open log file, if one was asked for; runGUI composes its own
// handler set and has to include it.
var logWriter *admin.RotatingFile

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
	// Init on an existing vault is a no-op whatever the passphrase, so a wrong
	// one used to look like success and every later command failed instead.
	if _, err := os.Stat(vaultPath); err == nil {
		existing, err := vault.Open(ctx, vaultPath, passphrase)
		if err != nil {
			return fmt.Errorf("a vault already exists here and this passphrase does not open it: %w", err)
		}
		existing.Close()
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
	fmt.Fprintf(os.Stdout, "Saved %d API key(s). A running gateway reloads them within a few seconds.\n", count)
	return nil
}

// openCodeModel carries what the alias declares, so a generated config is as
// capable as a hand-written one: limits, tools, vision and reasoning.
func openCodeModel(model router.Model) map[string]any {
	entry := map[string]any{"name": model.ID}
	limit := map[string]int{}
	if model.Context > 0 {
		limit["context"] = model.Context
	}
	if model.MaxOutput > 0 {
		limit["output"] = model.MaxOutput
	}
	if len(limit) > 0 {
		entry["limit"] = limit
	}
	input := []string{"text"}
	if model.Images > 0 {
		input = append(input, "image")
	}
	capabilities := map[string]any{"input": input, "output": []string{"text"}}
	if model.Tools > 0 {
		capabilities["tools"] = true
	}
	if model.Reasoning {
		capabilities["reasoning"] = map[string]any{"supported": true}
	}
	entry["capabilities"] = capabilities
	if model.Reasoning {
		entry["compatibility"] = map[string]string{
			"reasoningField": "reasoning_content", "maxTokensField": "max_tokens",
		}
	}
	return entry
}

func exportOpenCode(w io.Writer, listen string, models []router.Model) error {
	chat := make(map[string]any, len(models))
	messages := make(map[string]any, len(models))
	for _, model := range models {
		switch model.Protocol {
		case config.Chat:
			chat[model.ID] = openCodeModel(model)
		case config.Messages:
			messages[model.ID] = openCodeModel(model)
		}
	}
	if len(chat) == 0 && len(messages) == 0 {
		return errors.New("configure a chat or messages model and import a provider key before exporting")
	}
	providers := make(map[string]any, 2)
	if len(chat) > 0 {
		providers["allaiis"] = map[string]any{
			"name": "ALLAIIS", "package": "@opencode/ai/providers/openai-compatible",
			"env": []string{"ALLAIIS_API_KEY"},
			"settings": map[string]string{
				"baseURL": "http://" + listen + "/v1",
				"apiKey":  "{env:ALLAIIS_API_KEY}",
			},
			"models": chat,
		}
	}
	if len(messages) > 0 {
		providers["allaiis-anthropic"] = map[string]any{
			"name": "ALLAIIS (Anthropic)", "package": "@opencode/ai/providers/anthropic",
			"env": []string{"ALLAIIS_API_KEY"},
			"settings": map[string]string{
				"baseURL": "http://" + listen,
				"apiKey":  "{env:ALLAIIS_API_KEY}",
			},
			"models": messages,
		}
	}
	return writeJSON(w, map[string]any{
		"$schema":   "https://opencode.ai/config.json",
		"providers": providers,
	})
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// advertise prints the served aliases and warns about unpinned pools.
func advertise(w io.Writer, cfg config.Config, models []router.Model) {
	fmt.Fprintf(w, "Serving %d alias(es) on http://%s/v1\n", len(models), cfg.Listen)
	fmt.Fprintf(w, "Base URL: http://%s/v1\n", cfg.Listen)
	for _, model := range models {
		fmt.Fprintf(w, "  - %s (%s)\n", model.ID, model.Provider)
	}
	unpinned := 0
	for _, route := range cfg.Models {
		if route.CredentialLabel == "" {
			unpinned++
		}
	}
	if unpinned > 0 {
		fmt.Fprintf(w, "Hint: %d alias(es) have no --label; a multi-session pool then picks one by request hash.\n", unpinned)
	}
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
