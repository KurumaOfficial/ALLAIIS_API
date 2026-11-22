// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCatalogCarriesCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/user/client_attestation/mint" {
			fmt.Fprint(w, `{"tokens":[{"token":"attest"}]}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text":{"models":[
			{"id":"thinker","friendly_name":"Thinker","active":true,"usesCredits":true,"proOnly":true,
			 "maxContextLength":200000,"maxResponseTokens":8000,"maxImages":4,
			 "supportsReasoning":true,"supportsFunctionCalling":true,
			 "settings":{"temperature":0.4,"topP":0.8}},
			{"id":"retired","active":false}
		]}}`)
	}))
	t.Cleanup(server.Close)
	models, err := testClient(server).Catalog(context.Background(), Credential{DistinctID: "d"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models %d", len(models))
	}
	model := models[0]
	if !model.Reasoning || !model.Tools || !model.ProOnly || !model.UsesCredits ||
		model.MaxImages != 4 || model.ContextLimit != 200000 || model.MaxResponse != 8000 ||
		model.Temperature == nil || *model.Temperature != 0.4 || model.TopP == nil || *model.TopP != 0.8 {
		t.Fatalf("capabilities lost: %+v", model)
	}
}
