// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
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
	for _, route := range cfg.Models {
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
	}
	switch {
	case missing > 0:
		add("alias credentials", "error", fmt.Sprintf("%d alias(es) have no matching credential", missing))
	case unpinned > 0:
		add("alias credentials", "warn", fmt.Sprintf("%d alias(es) use an unpinned multi-key pool", unpinned))
	default:
		add("alias credentials", "ok", "every alias resolves to a credential")
	}

	repaired, unknown, names := nameStats()
	if live, ok := fetchStats(ctx, cfg.Listen); ok {
		// A running gateway knows better than this process does.
		repaired, unknown, names = live.repaired, live.unknown, live.names
	}
	if status, detail, ok := toolNameVerdict(repaired, unknown, firstNames(names, 3)); ok {
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
