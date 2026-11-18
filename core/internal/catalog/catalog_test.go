// SPDX-License-Identifier: GPL-3.0-only

package catalog

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
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

	models, err := List(t.Context(), server.URL+"?type=text", "DEEPSEEK V4.1 FLASH")
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{
		{ID: "deepseek-v4-1-flash-1017", Name: "DeepSeek v4.1 Flash (1017)"},
		{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash 1017"},
	}
	if !slices.Equal(models, want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
}
