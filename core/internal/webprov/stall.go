// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"
)

// ErrStreamStalled reports that the upstream stopped sending bytes.
var errStreamStalled = errors.New("the upstream stream stalled")

// ErrStreamStalled exposes the stall sentinel.
func ErrStreamStalled() error { return errStreamStalled }

// stallGuard cancels the read context when no bytes arrive within the window.
type stallGuard struct {
	cancel   context.CancelFunc
	timer    *time.Timer
	duration time.Duration
	fired    atomic.Bool
}

// newStallGuard returns the caller context unchanged for a zero window.
func newStallGuard(ctx context.Context, window time.Duration) (context.Context, *stallGuard) {
	if window <= 0 {
		return ctx, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	guard := &stallGuard{cancel: cancel, duration: window}
	guard.timer = time.AfterFunc(window, func() {
		guard.fired.Store(true)
		cancel()
	})
	return ctx, guard
}

func (g *stallGuard) wrap(reader io.Reader) io.Reader {
	if g == nil {
		return reader
	}
	return &idleReader{reader: reader, guard: g}
}

func (g *stallGuard) stop() {
	if g == nil {
		return
	}
	g.timer.Stop()
	g.cancel()
}

func (g *stallGuard) stalled() bool { return g != nil && g.fired.Load() }

// idleReader re-arms the watchdog on every byte.
type idleReader struct {
	reader io.Reader
	guard  *stallGuard
}

func (r *idleReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		r.guard.timer.Reset(r.guard.duration)
	}
	return n, err
}
