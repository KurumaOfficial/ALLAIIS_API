// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

// toolCallWarn is the history depth at which a request gets a warning header.
// It is deliberately a warning, not a cap: a legitimate multi-file task may
// need many calls, while a runaway agent should be visible to the operator.
const toolCallWarn = 40

// toolCallsInHistory counts the tool calls already present in a request. A
// parallel batch counts as several calls but still one assistant turn; the
// number is a rough depth signal, not a quota unit.
func toolCallsInHistory(protocol config.Protocol, body []byte) int {
	switch protocol {
	case config.Chat:
		var request struct {
			Messages []struct {
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage `json:"function_call"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &request) != nil {
			return 0
		}
		calls := 0
		for _, message := range request.Messages {
			var batch []json.RawMessage
			if json.Unmarshal(message.ToolCalls, &batch) == nil {
				calls += len(batch)
			}
			if present(message.FunctionCall) {
				calls++
			}
		}
		return calls
	case config.Messages:
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &request) != nil {
			return 0
		}
		calls := 0
		for _, message := range request.Messages {
			var blocks []struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(message.Content, &blocks) != nil {
				continue
			}
			for _, block := range blocks {
				if block.Type == "tool_use" {
					calls++
				}
			}
		}
		return calls
	case config.Responses:
		var request struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(body, &request) != nil {
			return 0
		}
		var items []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(request.Input, &items) != nil {
			return 0
		}
		calls := 0
		for _, item := range items {
			if item.Type == "function_call" {
				calls++
			}
		}
		return calls
	}
	return 0
}

func present(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null"))
}
