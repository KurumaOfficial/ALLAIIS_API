// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// metricEvent is one finished completion, kept for the local dashboard.
type metricEvent struct {
	Time     int64  `json:"time"`
	Protocol string `json:"protocol"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Status   int    `json:"status"`
	Millis   int64  `json:"millis"`
	Chars    int    `json:"chars"`
	Calls    int    `json:"calls"`
	Stream   bool   `json:"stream"`
}

type metrics struct {
	mu      sync.Mutex
	total   int64
	errors  int64
	chars   int64
	recent  []metricEvent
	byModel map[string]int64
}

const metricHistory = 200

func newMetrics() *metrics {
	return &metrics{byModel: map[string]int64{}}
}

func (m *metrics) add(event metricEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.total++
	if event.Status >= 400 {
		m.errors++
	}
	m.chars += int64(event.Chars)
	m.byModel[event.Model]++
	m.recent = append(m.recent, event)
	if len(m.recent) > metricHistory {
		m.recent = m.recent[len(m.recent)-metricHistory:]
	}
}

// summary reports totals and the newest events first.
func (m *metrics) summary() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	models := make([]map[string]any, 0, len(m.byModel))
	for id, count := range m.byModel {
		models = append(models, map[string]any{"model": id, "requests": count})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i]["requests"].(int64) > models[j]["requests"].(int64)
	})
	recent := make([]metricEvent, 0, len(m.recent))
	for i := len(m.recent) - 1; i >= 0; i-- {
		recent = append(recent, m.recent[i])
	}
	return map[string]any{
		"total": m.total, "errors": m.errors, "chars": m.chars,
		"models": models, "recent": recent,
	}
}

// statusWriter remembers the status code and fails writes after a 4xx.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// recordCompletion stores one metric after a handler returns.
func (g *Gateway) recordCompletion(w http.ResponseWriter, start time.Time, stream bool, target func() (string, string), chars, calls int) {
	status := http.StatusOK
	if recorder, ok := w.(*statusWriter); ok && recorder.status != 0 {
		status = recorder.status
	}
	model, provider := target()
	g.metrics.add(metricEvent{
		Time: time.Now().UnixMilli(), Model: model, Provider: provider,
		Status: status, Millis: time.Since(start).Milliseconds(),
		Chars: chars, Calls: calls, Stream: stream,
	})
}

// Metrics reports the request tally for the dashboard.
func (g *Gateway) Metrics() map[string]any { return g.metrics.summary() }
