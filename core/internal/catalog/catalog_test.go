// SPDX-License-Identifier: GPL-3.0-only

package catalog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func TestPublicCatalogFormats(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.URL.Query().Get("type") != "text" {
			t.Error("catalog request should use a public GET and preserve its query")
		}
		fmt.Fprint(w, `{"data":[
			{"id":"deepseek-v4.1-flash","name":"DeepSeek V4.1 Flash 1017"},
			{"id":"deepseek-v4-1-flash-1017","model_spec":{"name":"DeepSeek v4.1 Flash (1017)"}},
			{"id":"another-model","name":"Another model"}
		]}`)
	}))
	defer server.Close()

	models, err := List(t.Context(), server.URL+"?type=text", config.OpenAICatalog, "", "DEEPSEEK V4.1 FLASH")
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{
		{ID: "deepseek-v4-1-flash-1017", Name: "DeepSeek v4.1 Flash (1017)"},
		{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash 1017"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
}

func TestAuthenticatedCatalogPagination(t *testing.T) {
	tests := []struct {
		format config.CatalogFormat
		header string
		value  string
		cursor string
		next   string
		first  string
		last   string
	}{
		{
			format: config.ClaudeCatalog, header: "X-API-Key", value: "fixture-key",
			cursor: "after_id", next: "model-a",
			first: `{"data":[{"id":"model-a","display_name":"Model A"}],"has_more":true,"last_id":"model-a"}`,
			last:  `{"data":[{"id":"model-b","display_name":"Model B"}],"has_more":false}`,
		},
		{
			format: config.GeminiCatalog, header: "X-Goog-Api-Key", value: "fixture-key",
			cursor: "pageToken", next: "page-2",
			first: `{"models":[{"name":"models/model-a","displayName":"Model A","supportedGenerationMethods":["generateContent"]}],"nextPageToken":"page-2"}`,
			last:  `{"models":[{"name":"models/model-b","displayName":"Model B"}]}`,
		},
		{
			format: config.DashScopeCatalog, header: "Authorization", value: "Bearer fixture-key",
			cursor: "page_no", next: "2",
			first: `{"success":true,"output":{"page_no":1,"total":2,"models":[{"model":"model-a","name":"Model A"}]}}`,
			last:  `{"success":true,"output":{"page_no":2,"total":2,"models":[{"model":"model-b","name":"Model B"}]}}`,
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(tt.header) != tt.value ||
					(tt.format == config.ClaudeCatalog && r.Header.Get("Anthropic-Version") != "2023-06-01") {
					t.Error("catalog used the wrong authentication headers")
				}
				if r.URL.Query().Get(tt.cursor) == tt.next {
					fmt.Fprint(w, tt.last)
					return
				}
				fmt.Fprint(w, tt.first)
			}))
			defer server.Close()
			got, err := List(t.Context(), server.URL, tt.format, "fixture-key", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 2 || got[0].ID != "model-a" || got[0].Name != "Model A" ||
				got[1].ID != "model-b" || got[1].Name != "Model B" {
				t.Fatalf("invalid paginated result: %+v", got)
			}
			if tt.format == config.GeminiCatalog && !reflect.DeepEqual(got[0].Methods, []string{"generateContent"}) {
				t.Fatalf("missing Gemini model methods: %+v", got[0])
			}
		})
	}
}

func TestCatalogDoesNotFollowRedirectsWithCredentials(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("catalog followed a redirect")
	}))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	if _, err := List(t.Context(), upstream.URL, config.OpenAICatalog, "fixture-key", ""); err == nil {
		t.Fatal("expected a catalog error for the redirect")
	}
}
