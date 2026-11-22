// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/router"
)

func TestExportOpenCodeSplitsProtocols(t *testing.T) {
	models := []router.Model{
		{ID: "chat-alias", Protocol: config.Chat},
		{ID: "messages-alias", Protocol: config.Messages},
		{ID: "responses-alias", Protocol: config.Responses},
	}
	var buffer bytes.Buffer
	if err := exportOpenCode(&buffer, "127.0.0.1:8420", models); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Providers map[string]struct {
			Settings map[string]string `json:"settings"`
			Models   map[string]any    `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &config); err != nil {
		t.Fatalf("%v: %s", err, buffer.String())
	}
	chat, ok := config.Providers["allaiis"]
	if !ok || chat.Models["chat-alias"] == nil || chat.Models["messages-alias"] != nil {
		t.Fatalf("chat provider wrong: %+v", chat)
	}
	if chat.Settings["baseURL"] != "http://127.0.0.1:8420/v1" {
		t.Fatalf("chat base URL %q", chat.Settings["baseURL"])
	}
	anthropic, ok := config.Providers["allaiis-anthropic"]
	if !ok || anthropic.Models["messages-alias"] == nil {
		t.Fatalf("anthropic provider wrong: %+v", anthropic)
	}
	if anthropic.Settings["baseURL"] != "http://127.0.0.1:8420" {
		t.Fatalf("anthropic base URL %q", anthropic.Settings["baseURL"])
	}
	for name := range config.Providers {
		if strings.Contains(name, "responses") {
			t.Fatal("responses route exported without a client contract")
		}
	}
}

func TestExportOpenCodeRequiresSomething(t *testing.T) {
	var buffer bytes.Buffer
	if err := exportOpenCode(&buffer, "127.0.0.1:8420", nil); err == nil {
		t.Fatal("empty export succeeded")
	}
}
