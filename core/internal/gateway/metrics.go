// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
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
	series  map[seriesKey]*seriesValue
}

type seriesKey struct{ model, protocol, status string }

type seriesValue struct {
	count int64
	ms    int64
	chars int64
}

const metricHistory = 200

func protocolName(protocol config.Protocol) string {
	if protocol == config.Chat {
		return "chat"
	}
	return string(protocol)
}

func newMetrics() *metrics {
	return &metrics{byModel: map[string]int64{}, series: map[seriesKey]*seriesValue{}}
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
	key := seriesKey{model: event.Model, protocol: event.Protocol, status: strconv.Itoa(event.Status)}
	value := m.series[key]
	if value == nil {
		value = &seriesValue{}
		m.series[key] = value
	}
	value.count++
	value.ms += event.Millis
	value.chars += int64(event.Chars)
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
		"tool_unwrap":   toolCounters.unwrap.Load(),
		"tool_fold":     toolCounters.fold.Load(),
		"tool_suffix":   toolCounters.suffix.Load(),
		"tool_unknown":  toolCounters.unknown.Load(),
		"tool_ttl_days": toolNameTTL.Load(),
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
func (g *Gateway) recordCompletion(w http.ResponseWriter, start time.Time, protocol config.Protocol, stream bool, target func() (string, string), chars, calls int) {
	status := http.StatusOK
	if recorder, ok := w.(*statusWriter); ok && recorder.status != 0 {
		status = recorder.status
	}
	model, provider := target()
	g.metrics.add(metricEvent{
		Time: time.Now().UnixMilli(), Model: model, Provider: provider, Protocol: protocolName(protocol),
		Status: status, Millis: time.Since(start).Milliseconds(),
		Chars: chars, Calls: calls, Stream: stream,
	})
}

// Metrics reports the request tally for the dashboard.
func (g *Gateway) Metrics() map[string]any { return g.metrics.summary() }

// prometheus renders the counters in the text exposition format.
func (m *metrics) prometheus(models int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out strings.Builder
	out.WriteString("# HELP allaiis_requests_total Completion requests by alias, protocol and status.\n")
	out.WriteString("# TYPE allaiis_requests_total counter\n")
	keys := make([]seriesKey, 0, len(m.series))
	for key := range m.series {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		if keys[i].protocol != keys[j].protocol {
			return keys[i].protocol < keys[j].protocol
		}
		return keys[i].status < keys[j].status
	})
	for _, key := range keys {
		value := m.series[key]
		fmt.Fprintf(&out, "allaiis_requests_total{model=%q,protocol=%q,status=%q} %d\n",
			key.model, key.protocol, key.status, value.count)
	}
	out.WriteString("# TYPE allaiis_request_duration_milliseconds_sum counter\n")
	out.WriteString("# TYPE allaiis_response_characters_total counter\n")
	for _, key := range keys {
		value := m.series[key]
		fmt.Fprintf(&out, "allaiis_request_duration_milliseconds_sum{model=%q,status=%q} %d\n",
			key.model, key.status, value.ms)
		fmt.Fprintf(&out, "allaiis_response_characters_total{model=%q,status=%q} %d\n",
			key.model, key.status, value.chars)
	}
	// Counters for the reshaping the tool parser did, so its effect is visible
	// without reading the log.
	out.WriteString("# HELP allaiis_tool_unwrap_total Calls whose arguments had a wrapper removed.\n")
	out.WriteString("# TYPE allaiis_tool_unwrap_total counter\n")
	fmt.Fprintf(&out, "allaiis_tool_unwrap_total %d\n", toolCounters.unwrap.Load())
	out.WriteString("# HELP allaiis_tool_name_fold_total Calls whose unknown name was folded onto a tool.\n")
	out.WriteString("# TYPE allaiis_tool_name_fold_total counter\n")
	fmt.Fprintf(&out, "allaiis_tool_name_fold_total %d\n", toolCounters.fold.Load())
	out.WriteString("# HELP allaiis_tool_name_suffix_total Calls that lost a trailing word after the tool name.\n")
	out.WriteString("# TYPE allaiis_tool_name_suffix_total counter\n")
	fmt.Fprintf(&out, "allaiis_tool_name_suffix_total %d\n", toolCounters.suffix.Load())
	out.WriteString("# HELP allaiis_tool_unknown_total Calls delivered with a name no tool declares.\n")
	out.WriteString("# TYPE allaiis_tool_unknown_total counter\n")
	fmt.Fprintf(&out, "allaiis_tool_unknown_total %d\n", toolCounters.unknown.Load())
	out.WriteString("# HELP allaiis_models_served gauge\n")
	out.WriteString("# TYPE allaiis_models_served gauge\n")
	fmt.Fprintf(&out, "allaiis_models_served %d\n", models)
	out.WriteString("# TYPE allaiis_up gauge\n")
	out.WriteString("allaiis_up 1\n")
	return out.String()
}
