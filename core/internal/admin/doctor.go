// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// Check is one diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// diagnose runs cheap local checks before a request fails against them.
func (s *Server) diagnose(ctx context.Context) []Check {
	return Diagnose(ctx, s.options.Store, s.options.ConfigPath)
}

// nameStats is a seam so a test can supply parser counters and names.
var nameStats = func() (repaired, unknown int64, names []string) {
	repaired, unknown = gateway.ToolNameStats()
	return repaired, unknown, gateway.LiveUnknownNames(3)
}

// liveStats mirrors the tool counters of a running gateway, so a doctor started
// as its own process reports what the gateway saw instead of its own zeroes.
type liveStats struct {
	repaired int64
	unknown  int64
	names    []string
	expired  int
	ttl      time.Duration
}

// fetchLiveStats reads the tool counters from the gateway's /tool-names
// endpoint. It returns false when the gateway is unreachable or the body does
// not parse.
func fetchLiveStats(ctx context.Context, listen string) (liveStats, bool) {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listen+"/tool-names", nil)
	if err != nil {
		return liveStats{}, false
	}
	response, err := client.Do(request)
	if err != nil {
		return liveStats{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return liveStats{}, false
	}
	var body struct {
		Repaired   int64 `json:"repaired"`
		Unknown    int64 `json:"unknown"`
		TTLSeconds int64 `json:"ttl_seconds"`
		Names      []struct {
			Name       string `json:"name"`
			AgeSeconds int64  `json:"age_seconds"`
		} `json:"names"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return liveStats{}, false
	}

	stats := liveStats{repaired: body.Repaired, unknown: body.Unknown, ttl: time.Duration(body.TTLSeconds) * time.Second}
	for _, item := range body.Names {
		// A name past retention is no longer evidence, matching the in-process view.
		if stats.ttl > 0 && time.Duration(item.AgeSeconds)*time.Second > stats.ttl {
			stats.expired++
			continue
		}
		stats.names = append(stats.names, item.Name)
	}
	return stats, true
}

// fetchStats is a seam over the live fetch.
var fetchStats = fetchLiveStats

// Diagnose validates the local setup without spending provider quota.
func Diagnose(ctx context.Context, store *vault.Store, configPath string) []Check {
	return diagnose(ctx, store, configPath, true)
}

// DiagnoseOffline is Diagnose without the one network check, the enclave
// attestation.
func DiagnoseOffline(ctx context.Context, store *vault.Store, configPath string) []Check {
	return diagnose(ctx, store, configPath, false)
}

func diagnose(ctx context.Context, store *vault.Store, configPath string, attest bool) []Check {
	results := make([]Check, 0, 8)
	add := func(name, status, detail string) {
		results = append(results, Check{Name: name, Status: status, Detail: detail})
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		add("config", "error", err.Error())
		return results
	}
	add("config", "ok", fmt.Sprintf("%d alias(es) parsed and validated", len(cfg.Models)))

	if stamp, err := store.CredentialStamp(ctx); err != nil {
		add("vault", "error", err.Error())
	} else if stamp == "0:0" {
		add("vault", "warn", "the vault holds no credentials")
	} else {
		add("vault", "ok", "vault readable, credentials present")
	}

	if reachable(cfg.Listen) {
		add("gateway", "ok", "gateway accepts connections on "+cfg.Listen)
	} else {
		add("gateway", "error", "nothing listens on "+cfg.Listen)
	}

	webAliases := 0
	missing := 0
	unpinned := 0
	e2ee, e2eeOn := 0, 0
	images := 0
	var enclaves []enclaveTarget
	for _, route := range cfg.Models {
		if route.MaxImages > images {
			images = route.MaxImages
		}
		// The catalog has no flag for it; the vendor marks these with a prefix.
		if strings.HasPrefix(route.UpstreamModel, "e2ee-") {
			if route.E2EE {
				e2eeOn++
			} else {
				e2ee++
			}
		}
		provider, ok := cfg.Provider(route.Provider)
		if !ok {
			continue
		}
		if provider.Web() {
			webAliases++
		}
		entries, err := store.Entries(ctx, route.Provider, provider.BaseURL)
		if err != nil {
			add("credentials/"+route.ID, "error", err.Error())
			continue
		}
		if route.CredentialLabel == "" {
			if len(entries) == 0 {
				missing++
			} else if len(entries) > 1 {
				unpinned++
			}
			if route.E2EE && len(entries) > 0 && len(enclaves) < 3 {
				enclaves = append(enclaves, enclaveTarget{model: route.UpstreamModel, base: provider.BaseURL, entry: entries[0]})
			}
			continue
		}
		found := false
		for _, entry := range entries {
			if entry.Label == route.CredentialLabel {
				found = true
				break
			}
		}
		if !found {
			missing++
		}
		if route.E2EE && found && len(enclaves) < 3 {
			for _, entry := range entries {
				if entry.Label == route.CredentialLabel {
					enclaves = append(enclaves, enclaveTarget{model: route.UpstreamModel, base: provider.BaseURL, entry: entry})
					break
				}
			}
		}
	}
	switch {
	case missing > 0:
		add("alias credentials", "error", fmt.Sprintf("%d alias(es) have no matching credential", missing))
	case unpinned > 0:
		add("alias credentials", "warn", fmt.Sprintf("%d alias(es) use an unpinned multi-key pool", unpinned))
	default:
		add("alias credentials", "ok", "every alias resolves to a credential")
	}

	// Base64 costs a third on top of the file, so a body cap barely above a
	// single image rejects an album the model would take.
	if capMB := cfg.MaxRequestBytes() >> 20; images > 1 && capMB < 16 {
		add("request cap", "warn", fmt.Sprintf(
			"an alias takes up to %d images but the request cap is %d MiB; inline data URLs grow by a third, consider 16 or more", images, capMB))
	}

	// An attestation costs no provider quota, so the enclave itself can be
	// checked here; the reply path still needs balance.
	if len(enclaves) > 0 && attest {
		if status, detail, ok := enclaveVerdict(enclavesToChecks(ctx, enclaves)); ok {
			add("e2ee enclave", status, detail)
		}
	}

	switch {
	case e2ee > 0:
		add("e2ee models", "warn", fmt.Sprintf(
			"%d alias(es) point at an e2ee model without `e2ee: true`, so their prompts go out in plaintext", e2ee))
	case e2eeOn > 0:
		add("e2ee models", "ok", fmt.Sprintf(
			"%d alias(es) encrypt to the model enclave; the live end-to-end result is not verified yet", e2eeOn))
	}

	if routes, err := router.New(ctx, cfg, store); err != nil {
		add("credential cooldowns", "warn", "the pool is unreadable: "+err.Error())
	} else if statuses, err := routes.Status(ctx); err != nil {
		add("credential cooldowns", "warn", "the cooldowns are unreadable: "+err.Error())
	} else if status, detail, ok := cooldownVerdict(statuses); ok {
		add("credential cooldowns", status, detail)
	}

	repaired, unknown, names := nameStats()
	expired := 0
	ttl := liveTTL(time.Duration(cfg.ToolNameTTLDays) * 24 * time.Hour)
	if live, ok := fetchStats(ctx, cfg.Listen); ok {
		// A running gateway knows better than this process does.
		repaired, unknown, names, expired, ttl = live.repaired, live.unknown, live.names, live.expired, live.ttl
	}
	if status, detail, ok := autostartVerdict(); ok {
		add("autostart", status, detail)
	}
	if status, detail, ok := toolNameVerdict(repaired, unknown, firstNames(names, 3)); ok {
		// The retention decides how long a warning lives, so it travels with it.
		detail += "; " + retentionNote(ttl)
		if expired > 0 {
			detail += fmt.Sprintf("; %d older name(s) expired and ignored", expired)
		}
		add("tool names", status, detail)
	}

	if webAliases > 0 {
		if chromeReachable() {
			add("capture browser", "ok", "capture browser reachable on 9222")
		} else {
			add("capture browser", "warn", "DevTools port 9222 is closed; run `allaiis web chrome` before capturing")
		}
	}
	return results
}

// enclaveTarget is one alias whose enclave can be attested.
type enclaveTarget struct {
	model string
	base  string
	entry vault.Entry
}

// enclaveCheck is one attestation result.
type enclaveCheck struct {
	model    string
	provider string
	hardware string
	guest    bool
	err      error
}

// enclavesToChecks attests every target; an attestation spends no quota.
func enclavesToChecks(ctx context.Context, targets []enclaveTarget) []enclaveCheck {
	checks := make([]enclaveCheck, 0, len(targets))
	for _, target := range targets {
		client := webprov.New(10 * time.Second)
		// A loopback or dev endpoint speaks plain HTTP, which the Chrome
		// transport would dial as TLS.
		if strings.HasPrefix(target.base, "http://") {
			client = client.WithHTTP(http.DefaultClient)
		}
		check := enclaveCheck{model: target.model}
		cred, err := webprov.ParseCredential(target.entry.Key)
		if err != nil {
			check.err = err
			checks = append(checks, check)
			continue
		}
		probe, err := client.ProbeE2EE(ctx, cred, target.model)
		check.provider, check.hardware, check.err = probe.Provider, probe.Hardware, err
		// An enclave takes an encrypted prompt from any session, but the call
		// needs a signed-in one.
		check.guest = len(cred.Clerk) == 0
		checks = append(checks, check)
	}
	return checks
}

// enclaveVerdict summarises the attestations.
func enclaveVerdict(checks []enclaveCheck) (string, string, bool) {
	if len(checks) == 0 {
		return "", "", false
	}
	var bad, good, guest []string
	for _, check := range checks {
		if check.err != nil {
			bad = append(bad, check.model+": "+check.err.Error())
			continue
		}
		good = append(good, check.model+" ("+check.provider+"/"+check.hardware+")")
		if check.guest {
			guest = append(guest, check.model)
		}
	}
	if len(bad) > 0 {
		return "warn", fmt.Sprintf("%d of %d enclave(s) failed: %s", len(bad), len(checks), strings.Join(bad, "; ")), true
	}
	if len(guest) > 0 {
		return "warn", fmt.Sprintf("%d enclave(s) attested, but on a guest session that cannot run e2ee: %s",
			len(guest), strings.Join(guest, ", ")), true
	}
	return "ok", fmt.Sprintf("%d enclave(s) attested, a reply is unverified: %s", len(good), strings.Join(good, ", ")), true
}

// cooldownVerdict says which labels are parked and for how long, so a model
// that fails over a long wait is explained instead of guessed at.
func cooldownVerdict(statuses []router.ProviderStatus) (string, string, bool) {
	if len(statuses) == 0 {
		return "", "", false
	}
	var parked []string
	longest := time.Duration(0)
	blocked, pools := 0, 0
	for _, status := range statuses {
		if len(status.Cooldowns) == 0 {
			continue
		}
		cooled := 0
		for label, until := range status.Cooldowns {
			left := time.Until(until)
			if left <= 0 {
				continue
			}
			cooled++
			if left > longest {
				longest = left
			}
			parked = append(parked, status.Provider+"/"+label+" "+left.Round(time.Minute).String())
		}
		if cooled == 0 {
			continue
		}
		if cooled >= len(status.Credentials) {
			blocked++
		}
		pools++
	}
	if len(parked) == 0 {
		return "", "", false
	}
	sort.Strings(parked)
	count := len(parked)
	if len(parked) > 3 {
		parked = parked[:3]
	}
	detail := fmt.Sprintf("%d label(s) parked, longest %s: %s", count, longest.Round(time.Minute), strings.Join(parked, ", "))
	if blocked == pools {
		return "warn", detail + "; every label is parked, so calls answer 429 until one frees", true
	}
	return "ok", detail, true
}

// toolNameVerdict turns the name counters into a cheap diagnostic: a name the
// parser could not repair means the model invents tools the catalog lacks. The
// names themselves are listed because "bash" and "Read-File" call for different
// fixes.
func toolNameVerdict(repaired, unknown int64, names []string) (status, detail string, ok bool) {
	if repaired+unknown == 0 {
		return "", "", false
	}
	listed := ""
	if len(names) > 0 {
		listed = " (" + strings.Join(names, ", ") + ")"
	}
	if unknown > repaired {
		return "warn", fmt.Sprintf("%d call(s) named a tool outside the catalog%s, only %d were repaired", unknown, listed, repaired), true
	}
	return "ok", fmt.Sprintf("%d call(s) had a name repaired, %d stayed unknown%s", repaired, unknown, listed), true
}

// toolReset clears the tool name record, or only its expired names when the
// body asks for that.
func (s *Server) toolReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Expired bool `json:"expired"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Expired {
		writeJSON(w, http.StatusOK, map[string]any{"dropped": s.options.Gateway.DropExpiredToolNames()})
		return
	}
	s.options.Gateway.ResetToolNameState()
	writeJSON(w, http.StatusOK, map[string]any{"reset": true})
}

// autostartVerdict reports whether the gateway starts with the session, which a
// machine meant to keep serving needs.
func autostartVerdict() (string, string, bool) {
	if runtime.GOOS != "windows" {
		return "", "", false
	}
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", "", false
	}
	launcher := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "ALLAIIS-Gateway.cmd")
	if _, err := os.Stat(launcher); err == nil {
		return "ok", "registered to start at logon", true
	}
	return "warn", "not registered to start at logon; run allaiis service install", true
}

// retentionNote says how long a name stays evidence.
func retentionNote(ttl time.Duration) string {
	if ttl <= 0 {
		return "retention off, names are kept"
	}
	days := int(ttl / (24 * time.Hour))
	switch {
	case days > 0:
		return fmt.Sprintf("retention %dd", days)
	case ttl >= time.Hour:
		return fmt.Sprintf("retention %dh", int(ttl/time.Hour))
	case ttl >= time.Minute:
		return fmt.Sprintf("retention %dm", int(ttl/time.Minute))
	}
	return fmt.Sprintf("retention %ds", int(ttl/time.Second))
}

// liveTTL prefers the environment override over the configured retention.
func liveTTL(configured time.Duration) time.Duration {
	if raw := os.Getenv("ALLAIIS_TOOL_NAME_TTL"); raw != "" {
		if ttl, err := time.ParseDuration(raw); err == nil {
			return ttl
		}
	}
	return configured
}

// firstNames keeps the leading names up to limit.
func firstNames(names []string, limit int) []string {
	if len(names) > limit {
		return names[:limit]
	}
	return names
}

func (s *Server) doctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"checks": s.diagnose(r.Context())})
}

// chromeReachable reports whether the capture browser DevTools port answers.
func chromeReachable() bool {
	client := &http.Client{Timeout: 700 * time.Millisecond}
	response, err := client.Get("http://127.0.0.1:9222/json/version")
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}
