// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// e2eeServer stands in for the enclave: it owns the model key, checks the
// client's sealed message and answers with a sealed chunk of its own.
type e2eeServer struct {
	server  *httptest.Server
	private *secp256k1.PrivateKey
	nonceOK bool
	got     string
}

func newE2EEServer(t *testing.T, reply string) *e2eeServer {
	t.Helper()
	fake := &e2eeServer{nonceOK: true}
	private, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	fake.private = private
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/user/client_attestation/mint":
			fmt.Fprint(w, `{"tokens":[{"token":"attestation"}]}`)
		case "/v1/tee/attestation":
			nonce := r.URL.Query().Get("nonce")
			echo := nonce
			if !fake.nonceOK {
				echo = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, `{"verified":true,"nonce":%q,"signing_key":%q}`,
				echo, hex.EncodeToString(fake.private.PubKey().SerializeUncompressed()))
		case "/inference/chat":
			if r.Header.Get("X-Venice-TEE-Signing-Algo") != "ecdsa" {
				t.Errorf("signing algorithm header missing")
				return
			}
			clientKey, err := parseSecpKey(r.Header.Get("X-Venice-TEE-Client-Pub-Key"))
			if err != nil {
				t.Error(err)
				return
			}
			var body struct {
				Prompt []struct{ Content string } `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Prompt) != 1 {
				t.Errorf("bad request body: %v", err)
				return
			}
			sealed := body.Prompt[0].Content
			if !looksSealed(sealed) {
				t.Errorf("message went out in plaintext: %q", sealed)
				return
			}
			raw, _ := hex.DecodeString(sealed)
			ephemeral, err := secp256k1.ParsePubKey(raw[:65])
			if err != nil {
				t.Error(err)
				return
			}
			key, err := deriveE2EEKey(fake.private, ephemeral)
			if err != nil {
				t.Error(err)
				return
			}
			plain, err := openWith(key, raw[65:77], raw[77:])
			if err != nil {
				t.Error(err)
				return
			}
			fake.got = plain
			answer, err := sealTo(fake.private, clientKey, reply)
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(w, "{\"kind\":\"content\",\"content\":%q}\n", answer)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *e2eeServer) client(t *testing.T) *Client {
	t.Helper()
	return New(5 * time.Second).WithHTTP(f.server.Client()).WithBase(f.server.URL)
}

func TestE2EERoundTrip(t *testing.T) {
	fake := newE2EEServer(t, "sealed answer")
	client := fake.client(t)
	cred := Credential{UserAgent: "test"}
	var text strings.Builder
	err := client.Chat(context.Background(), cred, Request{
		Model: "e2ee-x", E2EE: true,
		Messages: []Message{{Role: "user", Content: "hello there"}},
	}, func(event Event) error {
		if event.Kind == "content" {
			text.WriteString(event.Content)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.got != "hello there" {
		t.Fatalf("enclave read %q", fake.got)
	}
	if text.String() != "sealed answer" {
		t.Fatalf("client read %q", text.String())
	}
}

func TestE2EERejectsAStaleNonce(t *testing.T) {
	fake := newE2EEServer(t, "unused")
	fake.nonceOK = false
	client := fake.client(t)
	err := client.Chat(context.Background(), Credential{}, Request{
		Model: "e2ee-x", E2EE: true,
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(Event) error { return nil })
	if !errors.Is(err, ErrE2EE) || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("stale nonce accepted: %v", err)
	}
}

// sealTo is the enclave side of seal: same derivation, opposite key.
func sealTo(private *secp256k1.PrivateKey, peer *secp256k1.PublicKey, plaintext string) (string, error) {
	ephemeral, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return "", err
	}
	key, err := deriveE2EEKey(ephemeral, peer)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append([]byte{}, ephemeral.PubKey().SerializeUncompressed()...)
	out = append(out, nonce...)
	sealed, err := sealWith(key, nonce, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(append(out, sealed...)), nil
}

func sealWith(key, nonce, plaintext []byte) ([]byte, error) {
	aead, err := gcmFor(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, nil), nil
}

func openWith(key, nonce, body []byte) (string, error) {
	aead, err := gcmFor(key)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// The attachment path must seal too, or an image turn leaks its text.
func TestE2EEImagesSealsAndOpens(t *testing.T) {
	private, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	signing := hex.EncodeToString(private.PubKey().SerializeUncompressed())
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/mint") {
			fmt.Fprint(w, `{"tokens":[{"token":"t"}]}`)
			return
		}
		switch r.URL.Path {
		case "/v1/tee/attestation":
			fmt.Fprintf(w, `{"verified":true,"nonce":%q,"signing_key":%q}`, r.URL.Query().Get("nonce"), signing)
		case "/inference/chat/agents/venice":
			clientKey, err := parseSecpKey(r.Header.Get("X-Venice-TEE-Client-Pub-Key"))
			if err != nil {
				t.Error(err)
				return
			}
			_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil {
				t.Fatal(err)
			}
			form, err := multipart.NewReader(r.Body, params["boundary"]).ReadForm(1 << 20)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Input struct {
					Messages []struct {
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"messages"`
				} `json:"input"`
			}
			if err := json.Unmarshal([]byte(form.Value["request"][0]), &body); err != nil {
				t.Fatal(err)
			}
			raw, _ := hex.DecodeString(body.Input.Messages[0].Content[0].Text)
			ephemeral, err := secp256k1.ParsePubKey(raw[:65])
			if err != nil {
				t.Error(err)
				return
			}
			key, err := deriveE2EEKey(private, ephemeral)
			if err != nil {
				t.Error(err)
				return
			}
			plain, err := openWith(key, raw[65:77], raw[77:])
			if err != nil {
				t.Error(err)
				return
			}
			got = plain
			answer, err := sealTo(private, clientKey, "sealed image answer")
			if err != nil {
				t.Error(err)
				return
			}
			fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", answer)
		}
	}))
	t.Cleanup(server.Close)

	client := New(5 * time.Second).WithHTTP(server.Client()).WithBase(server.URL)
	var text strings.Builder
	err = client.Chat(context.Background(), Credential{UserAgent: "t"}, Request{
		Model: "e2ee-x", E2EE: true,
		Messages: []Message{{
			Role: "user", Content: "what is this pls",
			Images: []Image{{Data: []byte("png"), MIME: "image/png"}},
		}},
	}, func(event Event) error {
		text.WriteString(event.Content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "what is this pls" || text.String() != "sealed image answer" {
		t.Fatalf("image turn: enclave %q, client %q", got, text.String())
	}
}
