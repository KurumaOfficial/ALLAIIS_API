// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

var httpClient = http.Client{Timeout: 20 * time.Second}

func newJSONRequest(ctx context.Context, url string, payload any) (*http.Request, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "ALLAIIS/smoke")
	return request, nil
}

// health keeps the last scheduled probe result per alias and reports a route
// that used to pass and now fails.
type health struct {
	mu      sync.Mutex
	last    map[string]probeState
	updated time.Time
	running bool
}

type probeState struct {
	OK     bool      `json:"ok"`
	Status int       `json:"status"`
	Detail string    `json:"detail"`
	Time   time.Time `json:"time"`
	Millis int64     `json:"millis"`
}

func newHealth() *health { return &health{last: map[string]probeState{}} }

func (h *health) snapshot() (map[string]probeState, time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	copied := make(map[string]probeState, len(h.last))
	for key, value := range h.last {
		copied[key] = value
	}
	return copied, h.updated, h.running
}

// record stores the results and returns aliases that regressed.
func (h *health) record(results []probeResult) []probeResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	regressed := make([]probeResult, 0)
	for _, result := range results {
		previous, known := h.last[result.Alias]
		if known && previous.OK && !result.OK {
			regressed = append(regressed, result)
		}
		h.last[result.Alias] = probeState{
			OK: result.OK, Status: result.Status, Detail: trimDetail(result),
			Time: time.Now(), Millis: result.Millis,
		}
	}
	h.updated = time.Now()
	h.running = false
	return regressed
}

func trimDetail(result probeResult) string {
	detail := result.Error
	if detail == "" {
		detail = result.Text
	}
	if len(detail) > 160 {
		detail = detail[:160]
	}
	return detail
}

// startSmoke runs a probe sweep every interval until ctx ends.
func (s *Server) startSmoke(ctx context.Context, every time.Duration, webhook string) {
	if every < time.Minute {
		every = time.Minute
	}
	go func() {
		timer := time.NewTicker(every)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			s.runScheduledSmoke(ctx, webhook)
		}
	}()
}

func (s *Server) runScheduledSmoke(ctx context.Context, webhook string) {
	s.health.mu.Lock()
	if s.health.running {
		s.health.mu.Unlock()
		return
	}
	s.health.running = true
	s.health.mu.Unlock()

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	results, passed, failed := s.smokeRun(runCtx, "")
	regressed := s.health.record(results)
	slog.Info("scheduled smoke", "passed", passed, "failed", failed, "regressions", len(regressed))
	for _, result := range regressed {
		slog.Warn("alias regressed", "alias", result.Alias, "status", result.Status, "detail", trimDetail(result))
	}
	if webhook != "" && (len(regressed) > 0 || failed > 0) {
		s.postWebhook(ctx, webhook, map[string]any{
			"passed": passed, "failed": failed,
			"regressions": regressed, "results": results,
			"time": time.Now().UTC(),
		})
	}
}

func (s *Server) postWebhook(ctx context.Context, url string, payload any) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := newJSONRequest(ctx, url, payload)
	if err != nil {
		slog.Warn("smoke webhook request failed", "error", err)
		return
	}
	response, err := (&httpClient).Do(request)
	if err != nil {
		slog.Warn("smoke webhook delivery failed", "error", err)
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		slog.Warn("smoke webhook rejected", "status", response.StatusCode)
	}
}
