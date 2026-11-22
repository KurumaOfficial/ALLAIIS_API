// SPDX-License-Identifier: GPL-3.0-only

package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type logLine struct {
	Seq     uint64 `json:"seq"`
	Time    int64  `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// logHub keeps a bounded log tail for the dashboard.
type logHub struct {
	mu    sync.Mutex
	lines []logLine
	seq   uint64
}

func newLogHub() *logHub { return &logHub{} }

func (h *logHub) add(level, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	h.lines = append(h.lines, logLine{Seq: h.seq, Time: time.Now().UnixMilli(), Level: level, Message: message})
	if len(h.lines) > 600 {
		h.lines = h.lines[len(h.lines)-600:]
	}
}

// since returns lines newer than seq and the newest sequence number.
func (h *logHub) since(seq uint64) ([]logLine, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	result := make([]logLine, 0, len(h.lines))
	for _, line := range h.lines {
		if line.Seq > seq {
			result = append(result, line)
		}
	}
	if len(result) > 200 {
		result = result[len(result)-200:]
	}
	return result, h.seq
}

// teeHandler mirrors records into the hub and a wrapped handler.
type teeHandler struct {
	hub   *logHub
	next  slog.Handler
	attrs []slog.Attr
}

// Tee mirrors slog output into the dashboard buffer.
func Tee(hub *logHub, next slog.Handler) slog.Handler {
	return &teeHandler{hub: hub, next: next}
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }

func (h *teeHandler) Handle(ctx context.Context, record slog.Record) error {
	message := record.Message
	var extra strings.Builder
	for _, attr := range h.attrs {
		fmt.Fprintf(&extra, " %s=%v", attr.Key, attr.Value)
	}
	record.Attrs(func(attr slog.Attr) bool {
		fmt.Fprintf(&extra, " %s=%v", attr.Key, attr.Value)
		return true
	})
	if extra.Len() > 0 {
		message += extra.String()
	}
	if len(message) > 2000 {
		message = message[:2000] + "…"
	}
	h.hub.add(record.Level.String(), message)
	if h.next != nil {
		return h.next.Handle(ctx, record)
	}
	return nil
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &clone
}

func (h *teeHandler) WithGroup(string) slog.Handler { return h }
