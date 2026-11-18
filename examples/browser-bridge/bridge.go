// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

const modelID = "browser-echo"

//go:embed worker.html worker.js
var assets embed.FS

type job struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	result   chan string
	claimed  bool
	finished bool
}

type bridge struct {
	mu      sync.Mutex
	pending *job
	timeout time.Duration
}

func newBridge(host, apiKey, workerKey string, timeout time.Duration) http.Handler {
	b := &bridge{timeout: timeout}
	mux := http.NewServeMux()
	origin := "http://" + host
	worker := requireKey(workerKey, origin)
	api := requireKey(apiKey, "")
	mux.HandleFunc("GET /{$}", asset("worker.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /worker.js", asset("worker.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("POST /worker/claim", worker(b.claim))
	mux.HandleFunc("POST /worker/reply", worker(b.reply))
	mux.HandleFunc("GET /v1/models", api(models))
	mux.HandleFunc("POST /v1/chat/completions", api(b.complete))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != host {
			fail(w, http.StatusForbidden, "Open the exact loopback address shown in the terminal.")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func requireKey(key, origin string) func(http.HandlerFunc) http.HandlerFunc {
	expected := sha256.Sum256([]byte(key))
	return func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if origin != "" && r.Header.Get("Origin") != origin {
				fail(w, http.StatusForbidden, "Worker requests require the bridge page's origin.")
				return
			}
			token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			digest := sha256.Sum256([]byte(token))
			if !ok || subtle.ConstantTimeCompare(digest[:], expected[:]) != 1 {
				fail(w, http.StatusUnauthorized, "Invalid bearer key.")
				return
			}
			handler(w, r)
		}
	}
}

func asset(name, mediaType string) http.HandlerFunc {
	body, err := assets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
	}
}

func models(w http.ResponseWriter, _ *http.Request) {
	respond(w, http.StatusOK, map[string]any{
		"object": "list", "data": []map[string]any{
			{"id": modelID, "object": "model", "created": 0, "owned_by": "allaiis-example"},
		},
	})
}

func (b *bridge) complete(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !readJSON(w, r, &request) {
		return
	}
	if request.Model != modelID {
		fail(w, http.StatusNotFound, "Model ID must be "+modelID+".")
		return
	}
	if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != "user" {
		fail(w, http.StatusBadRequest, "End the messages array with a user text message.")
		return
	}
	text := request.Messages[len(request.Messages)-1].Content
	if strings.TrimSpace(text) == "" || len(text) > 8<<10 {
		fail(w, http.StatusBadRequest, "Send a non-empty text message of at most 8 KiB.")
		return
	}
	j := &job{ID: "chatcmpl-" + rand.Text(), Text: text, result: make(chan string, 1)}
	b.mu.Lock()
	if b.pending != nil {
		b.mu.Unlock()
		fail(w, http.StatusServiceUnavailable, "The browser worker already has a request.")
		return
	}
	b.pending = j
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.pending = nil
		b.mu.Unlock()
	}()

	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
		fail(w, http.StatusGatewayTimeout, "The browser worker did not reply.")
	case answer := <-j.result:
		completion(w, j.ID, answer, request.Stream)
	}
}

func (b *bridge) claim(w http.ResponseWriter, _ *http.Request) {
	b.mu.Lock()
	j := b.pending
	if j == nil || j.claimed {
		b.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	j.claimed = true
	b.mu.Unlock()
	respond(w, http.StatusOK, j)
}

func (b *bridge) reply(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if input.Text == "" || len(input.Text) > (8<<10)+32 {
		fail(w, http.StatusBadRequest, "Invalid worker reply.")
		return
	}
	b.mu.Lock()
	j := b.pending
	if j == nil || j.ID != input.ID || !j.claimed || j.finished {
		b.mu.Unlock()
		fail(w, http.StatusConflict, "This request is no longer awaiting a worker reply.")
		return
	}
	j.finished = true
	j.result <- input.Text
	b.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func completion(w http.ResponseWriter, id, answer string, stream bool) {
	result := map[string]any{"id": id, "created": time.Now().Unix(), "model": modelID}
	if !stream {
		result["object"] = "chat.completion"
		result["choices"] = []map[string]any{
			{"index": 0, "message": map[string]string{"role": "assistant", "content": answer}, "finish_reason": "stop"},
		}
		respond(w, http.StatusOK, result)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	result["object"] = "chat.completion.chunk"
	for _, choice := range []map[string]any{
		{"index": 0, "delta": map[string]string{"role": "assistant", "content": answer}, "finish_reason": nil},
		{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"},
	} {
		result["choices"] = []map[string]any{choice}
		data, _ := json.Marshal(result)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if media != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "Send application/json.")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if decoder.Decode(value) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		fail(w, http.StatusBadRequest, "Invalid JSON request.")
		return false
	}
	return true
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func respond(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}
