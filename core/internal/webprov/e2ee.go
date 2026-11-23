// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"golang.org/x/crypto/hkdf"
)

// e2eeHKDFInfo is the label Venice derives its message keys with.
var e2eeHKDFInfo = []byte("ecdsa_encryption")

// ErrE2EE names a failure of the end-to-end encryption layer.
var ErrE2EE = errors.New("e2ee")

// E2EEProbe is what an attestation verified, for the operator to read.
type E2EEProbe struct {
	Provider string `json:"tee_provider,omitempty"`
	Hardware string `json:"tee_hardware,omitempty"`
	Upstream string `json:"upstream_model,omitempty"`
	Signing  string `json:"signing_key"`
	TDX      bool   `json:"tdx_checked"`
}

// e2eeSession is one verified attestation bound to a client key pair.
type e2eeSession struct {
	model    string
	probe    E2EEProbe
	private  *secp256k1.PrivateKey
	modelPub *secp256k1.PublicKey
	modelKey []byte
	key      []byte
	expires  time.Time
}

// e2eeCache keeps sessions per model; the TEE key changes rarely, so a short
// cache saves an attestation round trip per turn.
type e2eeCache struct {
	mu       sync.Mutex
	sessions map[string]*e2eeSession
}

const e2eeTTL = 5 * time.Minute

// clientPublicKey is the uncompressed SEC1 form Venice expects, 130 hex chars.
func (s *e2eeSession) clientPublicKey() string {
	return hex.EncodeToString(s.private.PubKey().SerializeUncompressed())
}

// e2eeAttestation answers the TEE attestation probe.
type e2eeAttestation struct {
	Verified  bool   `json:"verified"`
	Nonce     string `json:"nonce"`
	Signing   string `json:"signing_key"`
	PublicKey string `json:"signing_public_key"`
	Debug     bool   `json:"debug_mode"`
	// ServerVerification carries the enclave's own TDX verdict; it is checked
	// when present because "verified" alone is one field.
	Provider           string `json:"tee_provider"`
	Hardware           string `json:"tee_hardware"`
	Upstream           string `json:"upstream_model"`
	ServerVerification struct {
		TDX struct {
			Valid      bool `json:"valid"`
			Signature  bool `json:"signatureValid"`
			ChainValid bool `json:"certificateChainValid"`
		} `json:"tdx"`
	} `json:"server_verification"`
}

// e2eeSession creates a key pair and verifies the enclave attestation against a
// fresh nonce, returning a session with the derived message key.
func (c *Client) e2eeSession(ctx context.Context, cred Credential, model string) (*e2eeSession, error) {
	c.e2ee.mu.Lock()
	cached := c.e2ee.sessions[model]
	c.e2ee.mu.Unlock()
	if cached != nil && time.Now().Before(cached.expires) {
		return cached, nil
	}

	private, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("%w: key pair: %v", ErrE2EE, err)
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("%w: nonce: %v", ErrE2EE, err)
	}
	nonceHex := hex.EncodeToString(nonce)
	endpoint := c.base + "/v1/tee/attestation?model=" + url.QueryEscape(model) + "&nonce=" + nonceHex
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.headers(request, cred, "")
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: attestation: %v", ErrE2EE, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: attestation answered %d", ErrE2EE, response.StatusCode)
	}
	var body e2eeAttestation
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("%w: attestation unreadable", ErrE2EE)
	}
	if !body.Verified {
		return nil, fmt.Errorf("%w: the enclave is not verified", ErrE2EE)
	}
	// A replay would pass a stale verification, so the nonce must come back.
	if body.Nonce != nonceHex {
		return nil, fmt.Errorf("%w: the attestation nonce does not match", ErrE2EE)
	}
	if body.Debug {
		return nil, fmt.Errorf("%w: the enclave runs in debug mode", ErrE2EE)
	}
	if tdx := body.ServerVerification.TDX; tdx.Valid || tdx.Signature || tdx.ChainValid {
		if !tdx.Valid || !tdx.Signature || !tdx.ChainValid {
			return nil, fmt.Errorf("%w: the TDX verdict failed", ErrE2EE)
		}
	}
	modelHex := body.Signing
	if modelHex == "" {
		modelHex = body.PublicKey
	}
	modelKey, err := parseSecpKey(modelHex)
	if err != nil {
		return nil, err
	}
	key, err := deriveE2EEKey(private, modelKey)
	if err != nil {
		return nil, err
	}
	session := &e2eeSession{
		model: model, private: private, modelPub: modelKey, modelKey: modelKey.SerializeUncompressed(),
		key: key, expires: time.Now().Add(e2eeTTL),
		probe: E2EEProbe{Provider: body.Provider, Hardware: body.Hardware, Upstream: body.Upstream,
			Signing: modelHex, TDX: body.ServerVerification.TDX.Valid},
	}
	c.e2ee.mu.Lock()
	c.e2ee.sessions[model] = session
	c.e2ee.mu.Unlock()
	return session, nil
}

// ProbeE2EE verifies the enclave for a model and reports what was checked,
// without sending a message.
func (c *Client) ProbeE2EE(ctx context.Context, cred Credential, model string) (E2EEProbe, error) {
	c.forgetE2EE(model)
	session, err := c.e2eeSession(ctx, cred, model)
	if err != nil {
		return E2EEProbe{}, err
	}
	return session.probe, nil
}

// forgetE2EE drops a cached session, used when a chunk will not decrypt.
func (c *Client) forgetE2EE(model string) {
	c.e2ee.mu.Lock()
	delete(c.e2ee.sessions, model)
	c.e2ee.mu.Unlock()
}

// parseSecpKey accepts the 128 or 130 hex forms Venice may return.
func parseSecpKey(value string) (*secp256k1.PublicKey, error) {
	if len(value) == 128 {
		value = "04" + value
	}
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 65 || raw[0] != 4 {
		return nil, fmt.Errorf("%w: unusable model key", ErrE2EE)
	}
	key, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: unusable model key", ErrE2EE)
	}
	return key, nil
}

// deriveE2EEKey runs ECDH and HKDF-SHA256, the same derivation Venice documents.
func deriveE2EEKey(private *secp256k1.PrivateKey, public *secp256k1.PublicKey) ([]byte, error) {
	shared := secp256k1.GenerateSharedSecret(private, public)
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, nil, e2eeHKDFInfo), key); err != nil {
		return nil, fmt.Errorf("%w: key derivation: %v", ErrE2EE, err)
	}
	return key, nil
}

// seal encrypts one plaintext into the wire form: ephemeral key, nonce, ciphertext.
func (s *e2eeSession) seal(plaintext []byte) (string, error) {
	ephemeral, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return "", err
	}
	key, err := deriveE2EEKey(ephemeral, s.modelPub)
	if err != nil {
		return "", err
	}
	aead, err := gcmFor(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := append([]byte{}, ephemeral.PubKey().SerializeUncompressed()...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, nil)
	return hex.EncodeToString(out), nil
}

// open decrypts one chunk with the session key and the chunk's ephemeral key.
func (s *e2eeSession) open(ciphertext string) (string, error) {
	raw, err := hex.DecodeString(ciphertext)
	if err != nil || len(raw) < 65+12+16 {
		return "", fmt.Errorf("%w: not a sealed chunk", ErrE2EE)
	}
	ephemeral, err := secp256k1.ParsePubKey(raw[:65])
	if err != nil {
		return "", fmt.Errorf("%w: unusable chunk key", ErrE2EE)
	}
	key, err := deriveE2EEKey(s.private, ephemeral)
	if err != nil {
		return "", err
	}
	aead, err := gcmFor(key)
	if err != nil {
		return "", err
	}
	nonce, body := raw[65:77], raw[77:]
	plaintext, err := aead.Open(nil, nonce, body, nil)
	if err != nil {
		return "", fmt.Errorf("%w: chunk does not decrypt", ErrE2EE)
	}
	return string(plaintext), nil
}

// gcmFor builds the AES-256-GCM aead for one key.
func gcmFor(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// looksSealed reports whether a content chunk is in the sealed wire form, so
// plain metadata chunks pass through untouched.
func looksSealed(text string) bool {
	if len(text) < 186 || len(text)%2 != 0 {
		return false
	}
	for _, char := range text {
		switch {
		case char >= '0' && char <= '9', char >= 'a' && char <= 'f', char >= 'A' && char <= 'F':
		default:
			return false
		}
	}
	return true
}

// sealMessages encrypts every message, whatever its role: Venice rejects a
// request that still carries plaintext content.
func sealMessages(session *e2eeSession, messages []Message) ([]Message, error) {
	sealed := make([]Message, len(messages))
	for i, message := range messages {
		body, err := session.seal([]byte(message.Content))
		if err != nil {
			return nil, err
		}
		message.Content = body
		sealed[i] = message
	}
	return sealed, nil
}
