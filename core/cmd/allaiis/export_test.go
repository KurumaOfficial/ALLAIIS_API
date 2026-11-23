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

// TestExportOpenCodeCarriesCapabilities pins what a generated config should not
// lose next to a hand-written one: limits, tools, vision and reasoning.
func TestExportOpenCodeCarriesCapabilities(t *testing.T) {
	models := []router.Model{{
		ID: "deep", Protocol: config.Chat, Context: 524288, MaxOutput: 131072,
		Images: 10, Tools: 64, Reasoning: true,
	}}
	var buffer bytes.Buffer
	if err := exportOpenCode(&buffer, "127.0.0.1:8420", models); err != nil {
		t.Fatal(err)
	}
	var config struct {
		Providers map[string]struct {
			Models map[string]struct {
				Limit        map[string]int `json:"limit"`
				Capabilities struct {
					Tools     any      `json:"tools"`
					Input     []string `json:"input"`
					Reasoning struct {
						Supported bool `json:"supported"`
					} `json:"reasoning"`
				} `json:"capabilities"`
				Compatibility map[string]string `json:"compatibility"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &config); err != nil {
		t.Fatalf("%v: %s", err, buffer.String())
	}
	model := config.Providers["allaiis"].Models["deep"]
	if model.Limit["context"] != 524288 || model.Limit["output"] != 131072 {
		t.Fatalf("limits %+v", model.Limit)
	}
	if model.Capabilities.Tools != true || len(model.Capabilities.Input) != 2 ||
		!model.Capabilities.Reasoning.Supported {
		t.Fatalf("capabilities %+v", model.Capabilities)
	}
	if model.Compatibility["reasoningField"] != "reasoning_content" {
		t.Fatalf("compatibility %+v", model.Compatibility)
	}
}

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
