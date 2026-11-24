// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/admin"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
)

// version is a var so a release build can set it with -ldflags.
var version = "0.1.48"

// runGUI serves the gateway and the loopback control panel together.
func runGUI(ctx context.Context, dir, configPath, vaultPath, listen, ui string, openBrowser bool, smokeEvery time.Duration, smokeWebhook string, noToolProbe bool) error {
	passphrase, err := autoPassphrase(ctx, dir, configPath, vaultPath)
	if err != nil {
		return err
	}
	defer clear(passphrase)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if listen != "" {
		cfg.Listen = listen
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	store, err := vault.Open(ctx, vaultPath, passphrase)
	if err != nil {
		return fmt.Errorf("open vault (run allaiis init for a new vault): %w", err)
	}
	defer store.Close()
	routes, err := router.New(ctx, cfg, store)
	if err != nil {
		return err
	}

	live := gateway.New(routes, store.ClientKey(), cfg.MinPrivacy).WithVersion(version).WithConfig(cfg).WithToolNameTTLFor(retentionTTL(cfg, configPath)).WithStateDir(ctx, dir).WithReload(configPath, store)
	applyStoredProxies(ctx, store, live)
	panel, err := admin.New(admin.Options{
		Gateway: live, Store: store, ConfigPath: configPath, DataDir: dir,
		Passphrase: append([]byte{}, passphrase...), Version: version,
		SmokeEvery: smokeEvery, SmokeWebhook: smokeWebhook, NoToolProbe: noToolProbe,
	})
	if err != nil {
		return err
	}
	stderr := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	handlers := []slog.Handler{admin.Tee(panel.Logs(), stderr)}
	if logWriter != nil {
		handlers = append(handlers, slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	slog.SetDefault(slog.New(slog.NewMultiHandler(handlers...)))
	advertise(os.Stdout, cfg, routes.Models(cfg.MinPrivacy))

	run, cancel := context.WithCancel(ctx)
	defer cancel()
	go live.Watch(run)
	if smokeEvery > 0 {
		panel.StartSmoke(run, smokeEvery, smokeWebhook)
	}

	ready := make(chan string, 1)
	failures := make(chan error, 2)
	go func() {
		err := panel.Listen(run, ui, func(url string) {
			fmt.Fprintf(os.Stdout, "Control panel: %s\n", url)
			if openBrowser {
				if err := admin.OpenBrowser(url); err != nil {
					slog.Warn("could not open the browser", "error", err)
				}
			}
			ready <- url
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			failures <- err
		}
	}()
	select {
	case <-ready:
	case err := <-failures:
		return err
	case <-run.Done():
		return nil
	}
	go func() {
		if err := serve(run, cfg.Listen, live); err != nil && !errors.Is(err, context.Canceled) {
			failures <- err
		}
	}()
	select {
	case err := <-failures:
		return err
	case <-run.Done():
		return nil
	}
}
