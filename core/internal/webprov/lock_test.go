// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func lockFixture(t *testing.T, hold <-chan struct{}) (*Client, *atomic.Int32) {
	t.Helper()
	var inFlight atomic.Int32
	var peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"expiresAt":1,"tokens":[{"token":"attestation"}]}`)
		case "/inference/chat":
			current := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				peakNow := peak.Load()
				if current <= peakNow || peak.CompareAndSwap(peakNow, current) {
					break
				}
			}
			if hold != nil {
				<-hold
			}
			time.Sleep(20 * time.Millisecond)
			fmt.Fprint(w, `{"kind":"content","content":"ok"}`+"\n")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return testClient(server), &peak
}

func chatOnce(t *testing.T, client *Client, ctx context.Context) error {
	t.Helper()
	return client.Chat(ctx, Credential{DistinctID: "device"}, Request{
		Model: "m", Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(Event) error { return nil })
}

// TestChatTurnsSerialisePerCredential pins the one-generation-per-account rule:
// two turns on the same credential must not overlap on the wire.
func TestChatTurnsSerialisePerCredential(t *testing.T) {
	client, peak := lockFixture(t, nil)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := chatOnce(t, client, context.Background()); err != nil {
				t.Errorf("turn failed: %v", err)
			}
		}()
	}
	wait.Wait()
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak concurrency %d", got)
	}
}

// TestWaitingTurnHonoursCancellation pins the wait itself: a turn queued behind
// another must give up when its caller does, not sit until the first finishes.
func TestWaitingTurnHonoursCancellation(t *testing.T) {
	hold := make(chan struct{})
	client, _ := lockFixture(t, hold)
	first := make(chan error, 1)
	go func() { first <- chatOnce(t, client, context.Background()) }()

	// Give the first turn time to take the lock and reach the handler.
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := chatOnce(t, client, ctx); err == nil {
		t.Fatal("the queued turn waited past its deadline")
	}

	close(hold)
	if err := <-first; err != nil {
		t.Fatalf("the first turn failed: %v", err)
	}
}
