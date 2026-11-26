// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAgentBodyCarriesMediaParts(t *testing.T) {
	req := Request{
		RequestID: "abc", Model: "m",
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "what is this?", Images: []Image{{Data: []byte("png-bytes"), MIME: "image/png"}}},
		},
	}
	body, parts := agentBody(req)
	if len(parts) != 1 || parts[0].mime != "image/png" || string(parts[0].data) != "png-bytes" {
		t.Fatalf("parts: %+v", parts)
	}
	if !strings.Contains(string(body), `"media_id":"`+strings.TrimPrefix(parts[0].name, "media.")+`"`) || !strings.Contains(string(body), `"id":"mabc1"`) {
		t.Fatalf("payload lost the media reference: %s", body)
	}
	if !strings.Contains(string(body), `"role":"system"`) {
		t.Fatal("system turn was dropped")
	}
}

func TestAgentBodyIDsEveryMessage(t *testing.T) {
	req := Request{
		RequestID: "ids", Model: "m",
		Messages: []Message{
			{Role: "system", Content: "be brief"},
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "answer"},
			{Role: "user", Content: "look", Images: []Image{{Data: []byte("img"), MIME: "image/png"}}},
		},
	}
	body, _ := agentBody(req)
	var payload struct {
		Input struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Input.Messages) != len(req.Messages) {
		t.Fatalf("messages %d", len(payload.Input.Messages))
	}
	seen := map[string]bool{}
	for index, message := range payload.Input.Messages {
		if message.ID == "" {
			t.Fatalf("message %d has no id: %s", index, body)
		}
		if seen[message.ID] {
			t.Fatalf("message %d repeats id %q", index, message.ID)
		}
		seen[message.ID] = true
	}
}

func TestChatImagesSendsMultipart(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mint") {
			io.WriteString(w, `{"tokens":[{"token":"t"}]}`)
			return
		}
		if r.URL.Path != "/inference/chat/agents/venice" {
			t.Errorf("wrong endpoint %s", r.URL.Path)
		}
		calls.Add(1)
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type %q", r.Header.Get("Content-Type"))
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		form, err := reader.ReadForm(1 << 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(form.Value["request"]) != 1 || !strings.Contains(form.Value["request"][0], "input_image") {
			t.Errorf("request part missing: %v", form.Value)
		}
		found := false
		for name, files := range form.File {
			if !strings.HasPrefix(name, "media.") || len(files) != 1 {
				t.Errorf("media part %q", name)
				continue
			}
			found = true
			file, _ := files[0].Open()
			data, _ := io.ReadAll(file)
			if string(data) != "img" {
				t.Errorf("media bytes %q", data)
			}
		}
		if !found {
			t.Error("no media part")
		}
		io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Red.\"}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	var text strings.Builder
	err := testClient(server).Chat(context.Background(), Credential{},
		Request{RequestID: "x", Model: "m", Messages: []Message{{Role: "user", Content: "hi", Images: []Image{{Data: []byte("img"), MIME: "image/png"}}}}},
		func(event Event) error { text.WriteString(event.Content); return nil })
	if err != nil || text.String() != "Red." || calls.Load() != 1 {
		t.Fatalf("text=%q err=%v", text.String(), err)
	}
}

func TestAgentStreamFailureIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"tokens":[{"token":"t"}]}`)
	}))
	defer server.Close()
	err := decodeAgentStream(strings.NewReader(
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"workflow_failed\"}}}\n\n"),
		func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "workflow_failed") {
		t.Fatalf("err=%v", err)
	}
	err = decodeAgentStream(strings.NewReader(
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"Invalid input: User message with attachments must have a non-empty id field\"}}}\n\n"),
		func(Event) error { return nil })
	if !errors.Is(err, ErrBadRequest()) {
		t.Fatalf("invalid input was not typed: %v", err)
	}
}

func TestAgentStreamUsesEventLines(t *testing.T) {
	stream := "event: response.output_text.delta\ndata: {\"delta\":\"Hel\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"delta\":\"lo\"}\n\ndata: [DONE]\n\n"
	var text strings.Builder
	if err := decodeAgentStream(strings.NewReader(stream), func(e Event) error {
		if e.Kind == "content" {
			text.WriteString(e.Content)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello" {
		t.Fatalf("text %q", text.String())
	}
}

func TestAgentBodyMarksFiles(t *testing.T) {
	req := Request{RequestID: "z", Model: "m", Messages: []Message{
		{Role: "user", Content: "read", Images: []Image{{Data: []byte("pdf"), MIME: "application/pdf", Name: "a.pdf"}}},
	}}
	body, parts := agentBody(req)
	if len(parts) != 1 || parts[0].filename != "a.pdf" {
		t.Fatalf("parts %+v", parts)
	}
	if !strings.Contains(string(body), `"type":"input_file"`) || !strings.Contains(string(body), `"filename":"a.pdf"`) {
		t.Fatalf("file not marked: %s", body)
	}
}
