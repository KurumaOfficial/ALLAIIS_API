// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

var errToolRepair = errors.New("tool block needs a repair attempt")

// webCompletion serves a route whose provider has no documented API.
func (g *Gateway) webCompletion(w http.ResponseWriter, r *http.Request, protocol config.Protocol, target router.Target, payload []byte, stream bool) {
	if protocol != config.Chat {
		writeError(w, protocol, http.StatusBadRequest, "protocol_unsupported",
			"Web-transport providers serve Chat Completions only.")
		return
	}
	credential, err := webprov.ParseCredential(target.Entry.Key)
	if err != nil {
		writeError(w, protocol, http.StatusServiceUnavailable, "credential_invalid",
			"The stored web session is unreadable. Capture it again.")
		return
	}
	if credential.DistinctID == "" {
		credential.DistinctID = randomID()
	}
	request, reason, tools, err := webRequest(payload, target.Route.UpstreamModel)
	if err != nil {
		writeError(w, protocol, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	envelope := ""
	if len(tools) > 0 {
		envelope = toolEnvelope(reason.system, tools)
		request.Messages = reason.messages(envelope)
	}
	emulated := "usage_estimate"
	if len(tools) > 0 {
		emulated = "tools,usage_estimate"
	}
	w.Header().Set(tierHeader, "tier_c3")
	w.Header().Set("X-ALLAIIS-Provider", target.Route.Provider)
	w.Header().Set("X-ALLAIIS-Model", target.Route.ID)
	w.Header().Set("X-ALLAIIS-Emulated-Features", emulated)

	id := "chatcmpl-" + randomID()
	created := time.Now().Unix()
	sent := false
	finish := "stop"
	var calls []toolCall

	guard := &toolGuard{}
	guard.add(tools)

	var startStream func()
	emit := func(event webprov.Event) error {
		if event.ServingModel != "" {
			w.Header().Set("X-ALLAIIS-Upstream-Model", event.ServingModel)
		}
		switch event.Kind {
		case "reasoning":
			if !reason.reasoning {
				return nil
			}
			reason.reasoningChars += len([]rune(event.Content))
			if stream {
				startStream()
				writeChunk(w, id, target.Route.ID, created, map[string]any{"reasoning_content": event.Content})
			}
			return nil
		case "content":
			text := event.Content
			if len(tools) > 0 {
				safe, parsed, err := guard.Write(event.Content)
				if err != nil {
					return errToolRepair
				}
				text = safe
				if len(parsed) > 0 {
					calls = append(calls, parsed...)
					if stream {
						startStream()
						writeToolChunks(w, id, target.Route.ID, created, parsed, false)
					}
				}
			}
			if text == "" {
				return nil
			}
			reason.append(text)
			if stream {
				startStream()
				writeChunk(w, id, target.Route.ID, created, map[string]any{"content": text})
			}
			return nil
		}
		return nil
	}
	startStream = func() {
		if sent || !stream {
			return
		}
		sent = true
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
	}

	started := time.Now()
	attempts := 0
	for {
		attempts++
		ctx, cancel := context.WithCancel(r.Context())
		err = g.web.Chat(ctx, credential, request, emit)
		cancel()
		if errors.Is(err, webprov.ErrUnauthorized) {
			g.web.Forget(credential)
		}
		if errors.Is(err, errToolRepair) && attempts < 2 && !sent {
			request.Messages = repairMessages(reason.messages(envelope), reason.text.String())
			reason.text.Reset()
			continue
		}
		break
	}
	if len(tools) > 0 {
		if tail, parsed, parseErr := guard.Flush(); parseErr == nil {
			if tail != "" {
				reason.append(tail)
				if stream {
					startStream()
					writeChunk(w, id, target.Route.ID, created, map[string]any{"content": tail})
				}
			}
			calls = append(calls, parsed...)
			if len(parsed) > 0 && stream {
				startStream()
				writeToolChunks(w, id, target.Route.ID, created, parsed, false)
			}
		}
	}
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	if err != nil && sent {
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%q,\"type\":\"upstream_error\"}}\n\n", err.Error())
		fmt.Fprint(w, "data: [DONE]\n\n")
		slog.Warn("web provider stream failed", "provider", target.Route.Provider, "error", err)
		return
	}
	if err != nil {
		g.webError(w, protocol, target, err)
		return
	}
	if stream {
		startStream()
		if len(calls) > 0 {
			writeToolChunks(w, id, target.Route.ID, created, nil, true)
		}
		finished(w, id, target.Route.ID, created, finish)
		fmt.Fprint(w, "data: [DONE]\n\n")
	} else {
		message := map[string]any{"role": "assistant", "content": reason.text.String()}
		if len(calls) > 0 {
			message["content"] = nil
			message["tool_calls"] = calls
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": target.Route.ID,
			"choices": []any{map[string]any{"index": 0, "finish_reason": finish, "message": message}},
			"usage": map[string]any{
				"prompt_tokens": estimateChars(len(payload)), "completion_tokens": reason.chars(),
				"total_tokens": estimateChars(len(payload)) + reason.chars(), "estimated": true,
			},
		})
	}
	slog.Info("web completion", "provider", target.Route.Provider, "model", target.Route.ID,
		"stream", stream, "chars", reason.chars(), "calls", len(calls), "attempts", attempts,
		"duration_ms", time.Since(started).Milliseconds())
}

func (g *Gateway) webError(w http.ResponseWriter, protocol config.Protocol, target router.Target, err error) {
	switch {
	case errors.Is(err, webprov.ErrRateLimited):
		until := time.Now().Add(10 * time.Minute)
		persist, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if setErr := g.router.SetCooldown(persist, target, until); setErr != nil {
			slog.Warn("could not persist provider cooldown", "provider", target.Route.Provider)
		}
		rateLimited(w, protocol, until)
	case errors.Is(err, webprov.ErrUnauthorized):
		writeError(w, protocol, http.StatusBadGateway, "web_session_expired",
			"The web session expired. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrCloudflare):
		writeError(w, protocol, http.StatusBadGateway, "cloudflare_challenge",
			"Cloudflare rejected the stored clearance. Re-run the capture command for this account.")
	case errors.Is(err, webprov.ErrContextTooLong()):
		writeError(w, protocol, http.StatusBadRequest, "context_length_exceeded",
			"The prompt exceeds the model context window.")
	default:
		writeError(w, protocol, http.StatusBadGateway, "upstream_error", "The web provider failed: "+err.Error())
	}
}

// webReason accumulates the reply and keeps the request history for a repair.
type webReason struct {
	text           strings.Builder
	system         string
	turns          []webprov.Message
	reasoning      bool
	charsN         int
	reasoningChars int
}

func (r *webReason) append(text string) {
	r.text.WriteString(text)
	r.charsN += len([]rune(text))
}

func (r *webReason) chars() int { return r.charsN }

// messages rebuilds the turn list with the current system instruction.
func (r *webReason) messages(system string) []webprov.Message {
	built := append([]webprov.Message{}, r.turns...)
	if system == "" {
		return built
	}
	if len(built) > 0 && built[0].Role == "system" {
		built[0].Content = system
		return built
	}
	return append([]webprov.Message{{Role: "system", Content: system}}, built...)
}

func (r *webReason) finish(w http.ResponseWriter, id, model string, created int64) {
	finished(w, id, model, created, "stop")
}

func finished(w http.ResponseWriter, id, model string, created int64, reason string) {
	data, _ := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": reason}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func writeChunk(w http.ResponseWriter, id, model string, created int64, delta map[string]any) {
	data, _ := json.Marshal(map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}

// writeToolChunks emits tool deltas, or only the final empty delta when calls is nil.
func writeToolChunks(w http.ResponseWriter, id, model string, created int64, calls []toolCall, final bool) {
	if !final && len(calls) == 0 {
		return
	}
	delta := make([]any, 0, len(calls))
	for index, call := range calls {
		delta = append(delta, map[string]any{
			"index": index, "id": call.ID, "type": "function",
			"function": map[string]string{"name": call.Function.Name, "arguments": call.Function.Arguments},
		})
	}
	if len(delta) == 0 {
		return
	}
	writeChunk(w, id, model, created, map[string]any{"tool_calls": delta})
}

func estimateChars(n int) int {
	if n <= 0 {
		return 0
	}
	return n/4 + 1
}

func randomID() string {
	var raw [8]byte
	rand.Read(raw[:])
	return fmt.Sprintf("%x", raw[:])
}

// webRequest flattens an OpenAI payload into the web protocol fields.
func webRequest(payload []byte, model string) (webprov.Request, *webReason, []toolSpec, error) {
	var input struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Temperature *float64 `json:"temperature"`
		TopP        *float64 `json:"top_p"`
		MaxTokens   int      `json:"max_tokens"`
		Reasoning   struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		ReasoningEffort string     `json:"reasoning_effort"`
		Tools           []toolSpec `json:"tools"`
		ToolChoice      any        `json:"tool_choice"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return webprov.Request{}, nil, nil, errors.New("expected a JSON object")
	}
	request := webprov.Request{
		Model: model, Temperature: input.Temperature, TopP: input.TopP, MaxTokens: input.MaxTokens,
	}
	request.Reasoning = input.ReasoningEffort != "" || input.Reasoning.Effort != "" ||
		strings.Contains(strings.ToLower(model), "reasoning")
	reason := &webReason{reasoning: request.Reasoning}
	for _, message := range input.Messages {
		text := messageText(message.Content)
		switch message.Role {
		case "system", "developer":
			if reason.system == "" {
				reason.system = text
			} else {
				reason.system += "\n\n" + text
			}
		case "tool":
			role := "user"
			reason.turns = append(reason.turns, webprov.Message{Role: role, Content: text})
		case "assistant":
			if len(messageToolCalls(message.Content)) > 0 {
				text = renderToolCalls(text, message.Content)
			}
			reason.turns = append(reason.turns, webprov.Message{Role: "assistant", Content: text})
		default:
			reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: text})
		}
	}
	tools := input.Tools
	if choice, ok := input.ToolChoice.(string); ok && choice == "none" {
		tools = nil
	}
	if len(reason.turns) == 0 {
		reason.turns = append(reason.turns, webprov.Message{Role: "user", Content: ""})
	}
	request.Messages = reason.messages(reason.system)
	return request, reason, tools, nil
}

// messageText joins text parts and renders simple non-text parts.
func messageText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var joined strings.Builder
		for _, part := range parts {
			if part.Text != "" {
				joined.WriteString(part.Text)
			}
		}
		return joined.String()
	}
	return string(raw)
}

func messageToolCalls(raw json.RawMessage) []toolCall {
	var holder struct {
		ToolCalls []toolCall `json:"tool_calls"`
	}
	json.Unmarshal(raw, &holder)
	return holder.ToolCalls
}

// renderToolCalls replays an earlier assistant call as text the web model can read.
func renderToolCalls(text string, raw json.RawMessage) string {
	calls := messageToolCalls(raw)
	if len(calls) == 0 {
		return text
	}
	var block strings.Builder
	block.WriteString(text)
	block.WriteString("\n" + toolOpen + `{"calls":[`)
	for i, call := range calls {
		if i > 0 {
			block.WriteString(",")
		}
		fmt.Fprintf(&block, `{"name":%q,"arguments":%s}`, call.Function.Name, call.Function.Arguments)
	}
	block.WriteString(`]}` + toolClose)
	return block.String()
}
