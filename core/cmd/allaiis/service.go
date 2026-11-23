// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

const serviceName = "ALLAIIS-Gateway"

const serviceUsage = `allaiis service — run the gateway at logon

  allaiis service install    Register the gateway as a background task
  allaiis service remove     Unregister it
  allaiis service status     Show whether it is registered

On Linux and macOS install writes a unit file and prints how to enable it.
`

// serviceCommand builds the gateway command line for the background task.
func serviceCommand(dir string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// The panel is the control surface on this machine, so the service runs the
	// gateway with it, on its usual port.
	log := filepath.Join(dir, "logs", "gateway.log")
	return strings.Join([]string{exe, "gui", "--no-open", "--ui", "127.0.0.1:8431",
		"--log-file", log, "--log-max-mb", "32", "--log-keep", "5"}, " "), nil
}

// serviceEnv is the environment the task needs: the passphrase, asked for when
// it is not set, and the data directory when it is not the default.
func serviceEnv() (map[string]string, error) {
	passphrase := os.Getenv("ALLAIIS_VAULT_PASSPHRASE")
	if passphrase == "" {
		raw, err := readHidden("Vault passphrase", "ALLAIIS_VAULT_PASSPHRASE is required for an unattended service")
		if err != nil {
			return nil, err
		}
		passphrase = strings.TrimSpace(string(raw))
	}
	env := map[string]string{"ALLAIIS_VAULT_PASSPHRASE": passphrase}
	if data := os.Getenv("ALLAIIS_DATA_DIR"); data != "" {
		env["ALLAIIS_DATA_DIR"] = data
	}
	return env, nil
}

// runService registers, removes or reports the background gateway.
func runService(ctx context.Context, dir string, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, serviceUsage)
		return nil
	}
	action := args[0]
	if action == "help" || action == "--help" {
		fmt.Fprint(os.Stdout, serviceUsage)
		return nil
	}
	if action != "install" && action != "remove" && action != "status" {
		return fmt.Errorf("unknown service action %q", action)
	}
	if runtime.GOOS != "windows" {
		return serviceUnit(dir, action)
	}
	return serviceWindows(ctx, dir, action)
}

// listening reports whether something accepts TCP connections on address.
func listening(address string) bool {
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// serviceWindows uses the Startup folder: it needs no elevation, unlike the
// task scheduler on some machines. The login script calls a launcher that sets
// the environment and starts the gateway minimised.
func serviceWindows(ctx context.Context, dir string, action string) error {
	_ = ctx
	startup, err := windowsStartupDir()
	if err != nil {
		return err
	}
	launcher := filepath.Join(startup, serviceName+".cmd")
	switch action {
	case "status":
		if _, err := os.Stat(launcher); err == nil {
			fmt.Fprintf(os.Stdout, "registered at logon: %s\n", launcher)
		} else {
			fmt.Fprintln(os.Stdout, "not registered")
		}
		// Registration is not the same as running, and the difference is the
		// first thing an operator asks.
		address := "127.0.0.1:8420"
		if cfg, err := config.Load(filepath.Join(dir, "config.json")); err == nil && cfg.Listen != "" {
			address = cfg.Listen
		}
		if listening(address) {
			fmt.Fprintf(os.Stdout, "gateway listening on %s\n", address)
		} else {
			fmt.Fprintf(os.Stdout, "gateway not listening on %s\n", address)
		}
		return nil
	case "remove":
		if err := os.Remove(launcher); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(os.Stdout, "removed")
		return nil
	}
	line, err := serviceCommand(dir)
	if err != nil {
		return err
	}
	env, err := serviceEnv()
	if err != nil {
		return err
	}
	var script strings.Builder
	script.WriteString("@echo off\r\n")
	for name, value := range env {
		fmt.Fprintf(&script, "set \"%s=%s\"\r\n", name, value)
	}
	// Restart on exit: an unattended gateway that died must come back. The wait
	// is a ping, not `timeout`: timeout needs a console input handle and fails at
	// once under `start /min`, which would turn a crash into a tight loop.
	script.WriteString(":run\r\n" + line + "\r\nping -n 6 127.0.0.1 >nul\r\ngoto run\r\n")
	scriptPath := filepath.Join(dir, "service.cmd")
	if err := os.WriteFile(scriptPath, []byte(script.String()), 0o600); err != nil {
		return err
	}
	login := fmt.Sprintf("@echo off\r\nstart \"\" /min cmd /c \"%s\"\r\n", scriptPath)
	if err := os.WriteFile(launcher, []byte(login), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Registered %s to start at logon.\nScript: %s\nLogs: %s\n",
		serviceName, scriptPath, filepath.Join(dir, "logs", "gateway.log"))
	return nil
}

// windowsStartupDir is the per-user Startup folder.
func windowsStartupDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA is not set")
	}
	dir := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	return dir, os.MkdirAll(dir, 0o700)
}

// serviceUnit writes a systemd unit or a launchd plist and prints the few root
// commands left.
func serviceUnit(dir string, action string) error {
	switch action {
	case "status":
		fmt.Fprintln(os.Stdout, "unit files are written by install; check the service manager for current state")
		return nil
	case "remove":
		if runtime.GOOS == "darwin" {
			fmt.Fprintln(os.Stdout, "launchctl unload ~/Library/LaunchAgents/com.allaiis.gateway.plist && rm ~/Library/LaunchAgents/com.allaiis.gateway.plist")
			return nil
		}
		fmt.Fprintln(os.Stdout, "sudo systemctl disable --now allaiis && sudo rm /etc/systemd/system/allaiis.service")
		return nil
	}
	line, err := serviceCommand(dir)
	if err != nil {
		return err
	}
	env, err := serviceEnv()
	if err != nil {
		return err
	}
	var pairs strings.Builder
	for name, value := range env {
		fmt.Fprintf(&pairs, "<key>%s</key><string>%s</string>", name, value)
	}
	if runtime.GOOS == "darwin" {
		var args strings.Builder
		for _, part := range strings.Split(line, " ") {
			fmt.Fprintf(&args, "<string>%s</string>", part)
		}
		plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.allaiis.gateway</string>
  <key>ProgramArguments</key><array>%s</array>
  <key>EnvironmentVariables</key><dict>%s</dict>
  <key>RunAtLoad</key><true/><key>KeepAlive</key><true/>
</dict></plist>
`, args.String(), pairs.String())
		path := filepath.Join(dir, "com.allaiis.gateway.plist")
		if err := os.WriteFile(path, []byte(plist), 0o600); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "Wrote %s\nThen: cp it to ~/Library/LaunchAgents and run launchctl load -w ~/Library/LaunchAgents/com.allaiis.gateway.plist\n", path)
		return nil
	}
	var environment strings.Builder
	for name, value := range env {
		fmt.Fprintf(&environment, "Environment=%s=%s\n", name, value)
	}
	unit := fmt.Sprintf(`[Unit]
Description=ALLAIIS gateway
After=network-online.target

[Service]
Type=simple
%sExecStart=%s
Restart=always
RestartSec=3

[Install]
WantedBy=default.target
`, environment.String(), line)
	path := filepath.Join(dir, "allaiis.service")
	if err := os.WriteFile(path, []byte(unit), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Wrote %s\nThen: sudo cp it to /etc/systemd/system/allaiis.service && sudo systemctl daemon-reload && sudo systemctl enable --now allaiis\n", path)
	return nil
}
