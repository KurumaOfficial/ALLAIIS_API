// SPDX-License-Identifier: GPL-3.0-only

package gateway

import (
	"strings"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/webprov"
)

// FuzzProtocolBuilders feeds arbitrary payloads to the three request builders.
// They are the surface a client controls, and a panic there is a 500 rather
// than a clean invalid_request.
func FuzzProtocolBuilders(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"messages":[{"role":"user","content":"hi"}],"stop":["x"],"tool_choice":"none"}`,
		`{"input":[{"role":"developer","content":[{"type":"input_text","text":"x"}]}],"stop":"y"}`,
		`{"system":[{"type":"text","text":"s"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"a","input":{}}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
		`{"input":[{"type":"function_call_output","call_id":"c","output":{"a":1}}]}`,
		`{"tools":[{"type":"function","function":{"name":"x","parameters":{}}}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`,
	}
	for _, seed := range seeds {
		f.Add(uint8(0), seed)
		f.Add(uint8(1), seed)
		f.Add(uint8(2), seed)
	}
	f.Fuzz(func(t *testing.T, kind uint8, payload string) {
		if len(payload) > 8192 {
			t.Skip()
		}
		saved := fetchImageForParts
		fetchImageForParts = func(string) (webprov.Image, bool) { return webprov.Image{}, false }
		defer func() { fetchImageForParts = saved }()

		var (
			request webprov.Request
			err     error
		)
		switch kind % 3 {
		case 0:
			request, _, _, err = webRequest([]byte(payload), "alias")
		case 1:
			request, _, _, err = messagesRequest([]byte(payload), "alias")
		default:
			request, _, _, err = responsesRequest([]byte(payload), "alias")
		}
		if err != nil {
			return
		}
		if request.Model != "alias" || len(request.Messages) == 0 {
			t.Fatalf("built %+v", request)
		}
		if strings.Contains(request.Messages[0].Content, "\x00") {
			t.Fatalf("nul byte survived")
		}
	})
}
