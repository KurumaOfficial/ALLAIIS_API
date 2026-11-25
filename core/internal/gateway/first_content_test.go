// SPDX-License-Identifier: GPL-3.0-only

package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebFirstContentCharacterIsNotDropped pins the held first chunk: a
// one-rune first event must still reach the client, in JSON and in SSE.
func TestWebFirstContentCharacterIsNotDropped(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"Ч\"}\n")
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"ем могу помочь?\"}\n")
	})
	handler, store := webGateway(t, server.URL)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+webModel+`","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("json: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil || len(reply.Choices) == 0 {
		t.Fatalf("json body: %s", result.Body)
	}
	if got := reply.Choices[0].Message.Content; got != "Чем могу помочь?" {
		t.Fatalf("json content %q", got)
	}

	streamBody := `{"model":"` + webModel + `","messages":[{"role":"user","content":"hi"}],"stream":true}`
	stream := complete(handler, store.ClientKey(), "", streamBody)
	if stream.Code != http.StatusOK || !strings.Contains(stream.Body.String(), "Ч") ||
		!strings.Contains(stream.Body.String(), "ем могу помочь?") {
		t.Fatalf("stream: %d %s", stream.Code, stream.Body)
	}
}

// TestWebKeepsContentOnReasoningTransition pins the frame where Venice ends
// thinking and starts the answer: both fields are set, and the answer token
// must not be mistaken for more reasoning.
func TestWebKeepsContentOnReasoningTransition(t *testing.T) {
	server := veniceFixture(t, func(w http.ResponseWriter, body map[string]any) {
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"\",\"reasoning_content\":\"I should greet.\"}\n")
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"При\",\"reasoning_content\":\" No tool needed.\"}\n")
		fmt.Fprint(w, "{\"kind\":\"content\",\"content\":\"вет!\"}\n")
	})
	handler, store := webGateway(t, server.URL)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+webModel+`","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+store.ClientKey())
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("json: %d %s", result.Code, result.Body)
	}
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &reply); err != nil || len(reply.Choices) == 0 {
		t.Fatalf("json body: %s", result.Body)
	}
	if got := reply.Choices[0].Message.Content; got != "Привет!" {
		t.Fatalf("json content %q", got)
	}
}
