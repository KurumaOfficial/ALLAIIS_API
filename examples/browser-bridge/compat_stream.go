// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultReasoningLimit = 32000

type compatError struct {
	status       int
	kind         string
	code         string
	message      string
	streamAsData bool
}

func (e *compatError) Error() string { return e.message }

func invalidInput(code, message string) *compatError {
	return &compatError{status: http.StatusBadRequest, kind: "invalid_request_error", code: code, message: message}
}

func remoteFailure(code, message string) *compatError {
	return &compatError{status: http.StatusBadGateway, kind: "api_error", code: code, message: message}
}

var (
	errStopReading     = errors.New("stream finished")
	errReasoningBudget = errors.New("reasoning budget exceeded")
)

type tokenUsage struct {
	Prompt     int  `json:"prompt_tokens"`
	Completion int  `json:"completion_tokens"`
	Total      int  `json:"total_tokens"`
	Estimated  bool `json:"estimated"`
}

func estimateUsage(prompt, answer int) tokenUsage {
	// ceil(bytes / 3.5) with integer arithmetic.
	input, output := (2*prompt+6)/7, (2*answer+6)/7
	return tokenUsage{Prompt: input, Completion: output, Total: input + output, Estimated: true}
}

type compatResult struct {
	content   string
	reasoning string
	calls     []toolCall
	output    int
	usage     tokenUsage
}

type streamEvent struct {
	Type        string                `json:"type"`
	OutputIndex int                   `json:"output_index"`
	Item        struct{ Type string } `json:"item"`
	Delta       string                `json:"delta"`
	Detected    json.RawMessage       `json:"detected_tools"`
}

// The synthetic fixtures use the event names documented here:
// https://github.com/crisazas/opencode-venice-web/blob/main/docs/PROTOCOL.md#3-inference-response
func readEventStream(reader io.Reader, visit func(streamEvent) error) error {
	lines := bufio.NewScanner(reader)
	lines.Buffer(make([]byte, 8192), 1<<20)
	var name string
	var data []string
	size := 0
	first := true
	for lines.Scan() {
		line := lines.Text()
		if first {
			line = strings.TrimPrefix(line, "\ufeff")
			first = false
		}
		if line == "" {
			if len(data) > 0 {
				raw := strings.Join(data, "\n")
				if raw == "[DONE]" {
					return nil
				}
				var event streamEvent
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					return remoteFailure("invalid_stream", "The upstream sent an invalid event.")
				}
				if event.Type == "" {
					event.Type = name
				}
				if err := visit(event); err != nil {
					return err
				}
			}
			name, data, size = "", nil, 0
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			size += len(value) + 1
			if size > 1<<20 {
				return remoteFailure("invalid_stream", "An upstream event exceeded 1 MiB.")
			}
			data = append(data, value)
		}
	}
	if err := lines.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func readCompatibilityReply(body io.Reader, policy toolPolicy, reasoningLimit int) (compatResult, error) {
	var text, reasoning strings.Builder
	items := make(map[int]string)
	completed := false
	reasoningChars := 0
	sawText := false
	var result compatResult
	readErr := readEventStream(body, func(event streamEvent) error {
		switch event.Type {
		case "response.output_item.added":
			items[event.OutputIndex] = event.Item.Type
		case "response.output_item.done":
			delete(items, event.OutputIndex)
		case "response.output_text.delta", "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			item := items[event.OutputIndex]
			isReasoning := event.Type != "response.output_text.delta" || item == "reasoning"
			if !isReasoning && item != "" && item != "message" {
				return nil
			}
			if isReasoning {
				reasoning.WriteString(event.Delta)
				reasoningChars += utf8.RuneCountInString(event.Delta)
				if !sawText && reasoningChars > reasoningLimit {
					return errReasoningBudget
				}
			} else {
				text.WriteString(event.Delta)
				sawText = sawText || strings.TrimSpace(event.Delta) != ""
			}
			if text.Len()+reasoning.Len() > 2<<20 {
				return remoteFailure("stream_too_large", "The upstream output exceeded 2 MiB.")
			}
			if !isReasoning && strings.Contains(event.Delta, ">") {
				content, calls, err := policy.extract(text.String(), false)
				if len(calls) > 0 {
					result.content, result.calls = content, calls
					return errStopReading
				}
				return err
			}
		case "response.output_text.done":
			if items[event.OutputIndex] != "reasoning" {
				content, calls, err := policy.extract(text.String(), true)
				if len(calls) > 0 {
					result.content, result.calls = content, calls
					return errStopReading
				}
				return err
			}
		case "response.completed":
			completed = true
		case "error", "response.failed":
			return remoteFailure("upstream_stream_error", "The upstream reported a streaming error.")
		case "response.incomplete":
			return remoteFailure("upstream_stream_incomplete", "The upstream reported an incomplete response.")
		}
		return nil
	})
	result.reasoning = reasoning.String()
	result.output = text.Len() + reasoning.Len()
	if errors.Is(readErr, errStopReading) {
		return result, nil
	}
	result.content = text.String()
	if readErr == nil || errors.Is(readErr, io.ErrUnexpectedEOF) {
		content, calls, err := policy.extract(result.content, true)
		if len(calls) > 0 {
			result.content, result.calls = content, calls
			return result, nil
		}
		if err != nil {
			return result, err
		}
		if readErr == nil || completed {
			return result, nil
		}
		return result, remoteFailure("upstream_stream_truncated", "The upstream stream ended before completion.")
	}
	return result, readErr
}

// invoke receives only a prepared JSON body and a cancellation context. The
// compatibility tests supply a loopback fixture. No transport is registered.
func runCompatibility(ctx context.Context, input []byte, model fixtureModel, reasoningLimit int, log *slog.Logger,
	invoke func(context.Context, []byte) (*http.Response, error)) (result compatResult, err error) {
	start := time.Now()
	status, attempts, promptBytes, removed := http.StatusOK, 0, 0, 0
	payload, policy, removed, err := prepareCompatibility(input, model)
	defer func() {
		if log == nil {
			return
		}
		var kind *compatError
		if errors.As(err, &kind) {
			status = kind.status
		} else if err != nil {
			status = http.StatusBadGateway
		}
		log.Info("completion", "origin", "offline-compatibility", "request_bytes", len(input),
			"upstream_request_bytes", promptBytes, "status", status, "model", payload.Model,
			"content_chars", utf8.RuneCountInString(result.content), "reasoning_chars", utf8.RuneCountInString(result.reasoning),
			"tools", len(policy.tools), "calls", len(result.calls), "attempts", attempts, "nul_removed", removed,
			"duration_ms", time.Since(start).Milliseconds())
	}()
	if err != nil {
		return result, err
	}
	if reasoningLimit <= 0 {
		reasoningLimit = defaultReasoningLimit
	}
	sizes := []int{75, 50, 35}
	if strings.EqualFold(os.Getenv("ALLAIIS_CONTEXT_TRIM"), "off") {
		sizes = nil
	}
	base := payload
	var reminder string
	retriedTool, retriedReasoning := false, false
	for {
		sent := payload
		if reminder != "" {
			sent.Messages = appendTurn(slices.Clone(sent.Messages), turn{Role: "user", Content: reminder})
		}
		wire, err := json.Marshal(sent)
		if err != nil {
			return result, err
		}
		promptBytes = len(wire)
		attempts++
		reply, err := attemptCompatibility(ctx, wire, policy, reasoningLimit, invoke)
		reply.usage = estimateUsage(promptBytes, reply.output)
		switch {
		case errors.Is(err, errInvalidTool):
			if retriedTool {
				return reply, nil
			}
			retriedTool, reminder = true, policy.reminder()
		case errors.Is(err, errReasoningBudget):
			if retriedReasoning {
				failure := invalidInput("reasoning_limit_exceeded", "The reply exceeded the reasoning budget twice.")
				failure.streamAsData = true
				return reply, failure
			}
			retriedReasoning, reminder = true, "Keep your reasoning brief and produce the answer directly."
		case errors.Is(err, errContextTooLong):
			changed := false
			for len(sizes) > 0 {
				trimmed := trimHistory(base, sizes[0])
				sizes = sizes[1:]
				if !slices.Equal(trimmed.Messages, payload.Messages) {
					payload = trimmed
					changed = true
					break
				}
			}
			if !changed {
				return reply, invalidInput("context_length_exceeded", "prompt is too long")
			}
		default:
			return reply, err
		}
	}
}

var errContextTooLong = errors.New("upstream context too long")

func attemptCompatibility(ctx context.Context, body []byte, policy toolPolicy, limit int,
	invoke func(context.Context, []byte) (*http.Response, error)) (compatResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	response, err := invoke(ctx, body)
	if err != nil {
		return compatResult{}, err
	}
	defer func() {
		cancel()
		response.Body.Close()
	}()
	switch response.StatusCode {
	case http.StatusOK:
		media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if media != "text/event-stream" {
			break
		}
		return readCompatibilityReply(response.Body, policy, limit)
	case http.StatusRequestEntityTooLarge:
		var upstream struct{ Error string }
		raw, err := io.ReadAll(io.LimitReader(response.Body, 8193))
		if err == nil && len(raw) <= 8192 && json.Unmarshal(raw, &upstream) == nil &&
			(upstream.Error == "userPromptTooLong" || upstream.Error == "userMessageTooLong") {
			return compatResult{}, errContextTooLong
		}
		return compatResult{}, &compatError{status: response.StatusCode, kind: "invalid_request_error",
			code: "request_too_large", message: "The upstream rejected the request size."}
	case http.StatusForbidden:
		return compatResult{}, &compatError{status: response.StatusCode, kind: "invalid_request_error",
			code: "upstream_access_denied", message: "The upstream denied access."}
	}
	return compatResult{}, remoteFailure("unexpected_upstream_response", "The upstream returned an unexpected response.")
}

func writeCompatibility(w http.ResponseWriter, id, model string, result compatResult, stream bool, err error) {
	if err != nil {
		var failure *compatError
		if !errors.As(err, &failure) {
			failure = remoteFailure("upstream_unavailable", "The upstream request failed.")
		}
		value := map[string]any{"error": map[string]string{
			"type": failure.kind, "code": failure.code, "message": failure.message,
		}}
		if stream && failure.streamAsData {
			w.Header().Set("Content-Type", "text/event-stream")
			writeEvent(w, value)
			fmt.Fprint(w, "data: [DONE]\n\n")
		} else {
			respond(w, failure.status, value)
		}
		return
	}
	data := map[string]any{"id": id, "created": time.Now().Unix(), "model": model}
	message := map[string]any{"role": "assistant", "content": result.content}
	if result.reasoning != "" {
		message["reasoning_content"] = result.reasoning
	}
	finish := "stop"
	if len(result.calls) > 0 {
		finish = "tool_calls"
		if result.content == "" {
			message["content"] = nil
		}
		message["tool_calls"] = result.calls
	}
	if !stream {
		data["object"] = "chat.completion"
		data["choices"] = []map[string]any{{"index": 0, "message": message, "finish_reason": finish}}
		data["usage"] = result.usage
		respond(w, http.StatusOK, data)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	data["object"] = "chat.completion.chunk"
	if len(result.calls) > 0 {
		calls := make([]map[string]any, 0, len(result.calls))
		for i, call := range result.calls {
			calls = append(calls, map[string]any{"index": i, "id": call.ID, "type": call.Type, "function": call.Function})
		}
		message["tool_calls"] = calls
	}
	data["choices"] = []map[string]any{{"index": 0, "delta": message, "finish_reason": nil}}
	writeEvent(w, data)
	data["choices"] = []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}
	writeEvent(w, data)
	data["choices"] = []any{}
	data["usage"] = result.usage
	writeEvent(w, data)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func writeEvent(w io.Writer, data any) {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(data); err == nil {
		fmt.Fprintf(w, "data: %s\n", encoded.Bytes())
	}
}
