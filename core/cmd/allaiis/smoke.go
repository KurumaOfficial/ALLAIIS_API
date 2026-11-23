// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/gateway"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/vault"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

type smokeResult struct {
	Alias    string `json:"alias"`
	Protocol string `json:"protocol"`
	Attempt  int    `json:"attempt"`
	Status   int    `json:"status"`
	// Code is the machine-readable error code, so a script can tell a cooling
	// pool from a model the app does not offer.
	Code   string `json:"code,omitempty"`
	Millis int64  `json:"millis"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// runSmoke exercises every alias (or one alias, repeatedly) against an
// in-process gateway, so a live gateway and its port are not disturbed.
func runSmoke(ctx context.Context, dir, configPath, vaultPath, alias string, attempts int, timeout time.Duration, asJSON, noToolProbe bool) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	passphrase, err := readPassphrase()
	if err != nil {
		return err
	}
	defer clear(passphrase)
	store, err := vault.Open(ctx, vaultPath, passphrase)
	if err != nil {
		return fmt.Errorf("open vault: %w", err)
	}
	defer store.Close()
	clear(passphrase)

	routes, err := router.New(ctx, cfg, store)
	if err != nil {
		return err
	}
	web := webprov.New(10 * time.Minute)
	live := gateway.NewWithWeb(routes, store.ClientKey(), cfg.MinPrivacy, web)
	server := httptest.NewServer(live)
	defer server.Close()
	client := &http.Client{Timeout: timeout}

	targets := smokeTargets(cfg, store, alias)
	if len(targets) == 0 {
		return errors.New("no matching alias with a stored credential")
	}
	if attempts < 1 {
		attempts = 1
	}
	var results []smokeResult
	passed, failed := 0, 0
	var toolTarget config.Route
	for _, target := range targets {
		provider, _ := cfg.Provider(target.Provider)
		key := provider.BaseURL
		if target.CredentialLabel != "" {
			key += "\x00" + target.CredentialLabel
		}
		cooling, _ := store.Cooldown(ctx, target.Provider, key)
		if bare, err := store.Cooldown(ctx, target.Provider, provider.BaseURL); err == nil && bare.After(cooling) {
			cooling = bare
		}
		for attempt := 1; attempt <= attempts; attempt++ {
			result := smokeResult{Alias: target.ID, Protocol: string(target.Protocol), Attempt: attempt}
			if result.Protocol == "" {
				result.Protocol = "chat"
			}
			// A balance mark is per model, so a pinned route can read it; an
			// unpinned one lets the call report itself.
			var balance time.Time
			if target.CredentialLabel != "" {
				balance, _ = store.Cooldown(ctx, target.Provider, key+"\x00"+target.ID)
			}
			switch {
			case time.Now().Before(balance):
				result.Status = http.StatusTooManyRequests
				result.Code = "insufficient_balance"
				result.Detail = "model has no balance until " + balance.Format("15:04:05")
			case time.Now().Before(cooling):
				result.Status = http.StatusTooManyRequests
				result.Detail = "provider cooling until " + cooling.Format("15:04:05")
			default:
				result = smokeOne(ctx, client, server.URL, store.ClientKey(), target, attempt)
			}
			results = append(results, result)
			if result.OK {
				passed++
				if toolTarget.ID == "" {
					toolTarget = target
				}
			} else {
				failed++
			}
			if !asJSON {
				mark := "ok"
				if !result.OK {
					mark = "FAIL"
				}
				fmt.Fprintf(os.Stdout, "%-34s %-10s %3d %6dms %-4s %s\n",
					result.Alias, result.Protocol, result.Attempt, result.Millis, mark, result.Detail)
			}
		}
	}
	// One tool probe per run: an alias that answers words can still lose the
	// parser, and that is what this command exists to catch.
	if toolTarget.ID != "" && !noToolProbe {
		tool := smokeTools(ctx, client, server.URL, store.ClientKey(), toolTarget)
		results = append(results, tool)
		if !asJSON {
			mark := "ok"
			if !tool.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(os.Stdout, "%-34s %-10s %3d %6dms %-4s %s\n",
				tool.Alias, tool.Protocol, tool.Attempt, tool.Millis, mark, tool.Detail)
		}
		if tool.OK {
			passed++
		} else {
			failed++
		}
	}
	if asJSON {
		if err := writeJSON(os.Stdout, map[string]any{"results": results, "passed": passed, "failed": failed}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stdout, "\n%d passed, %d failed\n", passed, failed)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d probe(s) failed", failed, passed+failed)
	}
	return nil
}

// smokeTargets lists configured routes that have a credential and match alias.
func smokeTargets(cfg config.Config, store *vault.Store, alias string) []config.Route {
	labels := map[string][]string{}
	targets := make([]config.Route, 0, len(cfg.Models))
	for _, route := range cfg.Models {
		if route.Disabled || (alias != "" && route.ID != alias) {
			continue
		}
		provider, ok := cfg.Provider(route.Provider)
		if !ok {
			continue
		}
		names, known := labels[route.Provider]
		if !known {
			entries, err := store.Entries(context.Background(), route.Provider, provider.BaseURL)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				names = append(names, entry.Label)
			}
			labels[route.Provider] = names
		}
		if route.CredentialLabel == "" {
			if len(names) == 0 {
				continue
			}
		} else if !containsString(names, route.CredentialLabel) {
			continue
		}
		targets = append(targets, route)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return targets
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// smokeCode reads the machine code out of an error body, empty when there is
// none or the body is not JSON.
func smokeCode(raw []byte) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	return body.Error.Code
}

func smokeOne(ctx context.Context, client *http.Client, base, key string, route config.Route, attempt int) smokeResult {
	result := smokeResult{Alias: route.ID, Protocol: string(route.Protocol), Attempt: attempt}
	if result.Protocol == "" {
		result.Protocol = "chat"
	}
	path := "/v1/chat/completions"
	body := map[string]any{
		"model": route.ID, "max_tokens": 32, "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": smokePrompt}},
	}
	headers := map[string]string{"Authorization": "Bearer " + key}
	switch route.Protocol {
	case config.Messages:
		path = "/v1/messages"
		body = map[string]any{
			"model": route.ID, "max_tokens": 32,
			"messages": []any{map[string]any{"role": "user", "content": smokePrompt}},
		}
		headers["Anthropic-Version"] = "2023-06-01"
	case config.Responses:
		path = "/v1/responses"
		body = map[string]any{"model": route.ID, "input": smokePrompt, "max_output_tokens": 32}
	}
	payload, _ := json.Marshal(body)
	requestCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, base+path, bytes.NewReader(payload))
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	request.Header.Set("Content-Type", "application/json")
	start := time.Now()
	response, err := client.Do(request)
	result.Millis = time.Since(start).Milliseconds()
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	result.Status = response.StatusCode
	if readable := response.Header.Get("X-ALLAIIS-Upstream-Model"); readable != "" {
		result.Detail = "upstream " + readable
	}
	if response.StatusCode != http.StatusOK {
		result.Code = smokeCode(raw)
		result.Detail = strings.TrimSpace(smokeFailure(raw))
		return result
	}
	text := smokeAnswer(raw, route.Protocol)
	if strings.TrimSpace(text) == "" {
		result.Detail = "empty answer"
		return result
	}
	result.OK = true
	if len(text) > 90 {
		text = text[:90] + "…"
	}
	if result.Detail == "" {
		result.Detail = strings.Join(strings.Fields(text), " ")
	}
	return result
}

const smokePrompt = "Reply with the single word: ready"

func smokeAnswer(raw []byte, protocol config.Protocol) string {
	switch protocol {
	case config.Messages:
		var reply struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		json.Unmarshal(raw, &reply)
		var built strings.Builder
		for _, block := range reply.Content {
			built.WriteString(block.Text)
		}
		return built.String()
	case config.Responses:
		var reply struct {
			Output []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
		}
		json.Unmarshal(raw, &reply)
		var built strings.Builder
		for _, item := range reply.Output {
			for _, part := range item.Content {
				built.WriteString(part.Text)
			}
		}
		return built.String()
	default:
		var reply struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		json.Unmarshal(raw, &reply)
		if len(reply.Choices) > 0 {
			return reply.Choices[0].Message.Content
		}
	}
	return ""
}

func smokeFailure(raw []byte) string {
	var reply struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &reply) == nil && reply.Error.Message != "" {
		return reply.Error.Message
	}
	text := strings.TrimSpace(string(raw))
	if len(text) > 160 {
		text = text[:160]
	}
	if text == "" {
		return "empty error body"
	}
	return text
}

// smokeTools asks an alias to call a tool and reports whether a call with the
// exact name came back. A prose answer gets one firmer retry.
func smokeTools(ctx context.Context, client *http.Client, base, key string, route config.Route) smokeResult {
	prompts := []string{
		"Call the tool list_files once with path . and nothing else. Do not answer in prose.",
		"Use the list_files tool. Arguments: {\"path\": \".\"}. Reply with the tool call only, no prose.",
	}
	var result smokeResult
	for attempt, prompt := range prompts {
		result = smokeToolAttempt(ctx, client, base, key, route, prompt, attempt+1)
		if result.OK || result.Status != http.StatusOK {
			break
		}
	}
	return result
}

func smokeToolAttempt(ctx context.Context, client *http.Client, base, key string, route config.Route, prompt string, attempt int) smokeResult {
	result := smokeResult{Alias: route.ID + " (tools)", Protocol: string(route.Protocol), Attempt: attempt}
	if result.Protocol == "" {
		result.Protocol = "chat"
	}
	payload, _ := json.Marshal(map[string]any{
		"model": route.ID, "max_tokens": 128, "stream": false,
		"messages": []any{map[string]any{"role": "user", "content": prompt}},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name":        "list_files",
			"description": "List the files in a directory",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
				"required":   []string{"path"},
			},
		}}},
	})
	requestCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ALLAIIS-Probe", "smoke")
	start := time.Now()
	response, err := client.Do(request)
	result.Millis = time.Since(start).Milliseconds()
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	result.Status = response.StatusCode
	if response.StatusCode != http.StatusOK {
		result.Code = smokeCode(raw)
		result.Detail = strings.TrimSpace(smokeFailure(raw))
		return result
	}
	name, arguments := smokeToolCall(raw)
	switch {
	case name == "":
		result.Detail = "answered without a tool call"
	case name != "list_files":
		result.Detail = "called " + name + " instead of list_files"
	case !strings.Contains(arguments, `"path"`):
		result.Detail = "called list_files without path"
	default:
		result.OK = true
		result.Detail = strings.TrimSpace(name + " " + arguments)
	}
	return result
}

// smokeToolCall reads the tool call out of a chat reply.
func smokeToolCall(raw []byte) (string, string) {
	var reply struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &reply) != nil || len(reply.Choices) == 0 {
		return "", ""
	}
	calls := reply.Choices[0].Message.ToolCalls
	if len(calls) == 0 {
		return "", ""
	}
	return calls[0].Function.Name, calls[0].Function.Arguments
}
