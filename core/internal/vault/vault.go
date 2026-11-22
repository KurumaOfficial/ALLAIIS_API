// SPDX-License-Identifier: GPL-3.0-only

package vault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

const schema = `
CREATE TABLE IF NOT EXISTS vault (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL,
	salt BLOB NOT NULL,
	client_key BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS credentials (
	provider TEXT NOT NULL,
	endpoint TEXT NOT NULL,
	label TEXT NOT NULL,
	fingerprint BLOB NOT NULL,
	ciphertext BLOB NOT NULL,
	PRIMARY KEY (provider, endpoint, label),
	UNIQUE (provider, endpoint, fingerprint)
);
CREATE TABLE IF NOT EXISTS response_turns (
	id TEXT PRIMARY KEY,
	ciphertext BLOB NOT NULL,
	expires_unix_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS cooldowns (
	provider TEXT NOT NULL,
	endpoint TEXT NOT NULL,
	until_unix_ms INTEGER NOT NULL,
	PRIMARY KEY (provider, endpoint)
);
`

type Entry struct {
	Provider string
	Endpoint string
	Label    string
	Key      string
}

type Store struct {
	db             *sql.DB
	aead           cipher.AEAD
	fingerprintKey []byte
	mu             sync.Mutex
	clientKey      string
}

func Init(ctx context.Context, path string, passphrase []byte) error {
	if len(passphrase) < 16 {
		return errors.New("vault passphrase must be at least 16 bytes")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err == nil {
		if err := file.Close(); err != nil {
			return err
		}
	}
	db, err := openDB(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()

	salt := make([]byte, 16)
	rand.Read(salt)
	aead, _ := derive(passphrase, salt)
	key := aead.Seal(nil, nil, []byte("sk-allaiis-"+rand.Text()), []byte("client-key/v1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vault VALUES (1, 1, ?, ?) ON CONFLICT(id) DO NOTHING`, salt, key); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, _, _, err = unlock(ctx, db, passphrase)
	return err
}

func Open(ctx context.Context, path string, passphrase []byte) (*Store, error) {
	db, err := openDB(ctx, path)
	if err != nil {
		return nil, err
	}
	aead, fingerprintKey, key, err := unlock(ctx, db, passphrase)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, aead: aead, fingerprintKey: fingerprintKey, clientKey: key}, nil
}

func openDB(ctx context.Context, path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	path = filepath.ToSlash(absolute)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func unlock(ctx context.Context, db *sql.DB, passphrase []byte) (cipher.AEAD, []byte, string, error) {
	var version int
	var salt, sealedKey []byte
	if err := db.QueryRowContext(ctx, `SELECT version, salt, client_key FROM vault WHERE id = 1`).
		Scan(&version, &salt, &sealedKey); err != nil {
		return nil, nil, "", fmt.Errorf("read vault metadata: %w", err)
	}
	if version != 1 || len(salt) != 16 {
		return nil, nil, "", errors.New("unsupported vault format")
	}
	aead, fingerprintKey := derive(passphrase, salt)
	key, err := aead.Open(nil, nil, sealedKey, []byte("client-key/v1"))
	if err != nil {
		return nil, nil, "", errors.New("incorrect vault passphrase or damaged vault")
	}
	return aead, fingerprintKey, string(key), nil
}

func derive(passphrase, salt []byte) (cipher.AEAD, []byte) {
	key := argon2.IDKey(passphrase, salt, 3, 64*1024, 4, 64)
	block, _ := aes.NewCipher(key[:32])
	aead, _ := cipher.NewGCMWithRandomNonce(block)
	return aead, key[32:]
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ClientKey() string { return s.clientKey }

// RotateClientKey seals a fresh gateway key and returns it.
func (s *Store) RotateClientKey(ctx context.Context) (string, error) {
	key := "sk-allaiis-" + rand.Text()
	sealed := s.aead.Seal(nil, nil, []byte(key), []byte("client-key/v1"))
	if _, err := s.db.ExecContext(ctx, `UPDATE vault SET client_key = ? WHERE id = 1`, sealed); err != nil {
		return "", err
	}
	s.mu.Lock()
	s.clientKey = key
	s.mu.Unlock()
	return key, nil
}

// CredentialStamp changes whenever the credential set changes, also from
// another process, so a running gateway can notice edits it did not make.
func (s *Store) CredentialStamp(ctx context.Context) (string, error) {
	var count, sum int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(rowid), 0) FROM credentials`).Scan(&count, &sum)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(count, 10) + ":" + strconv.FormatInt(sum, 10), nil
}

func (s *Store) Import(ctx context.Context, entries []Entry) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	count := 0
	for _, entry := range entries {
		if !config.ValidCredentialLabel(entry.Label) || !validKey(entry.Key) {
			return 0, fmt.Errorf("invalid label or API key for provider %q", entry.Provider)
		}
		mac := hmac.New(sha256.New, s.fingerprintKey)
		mac.Write([]byte(entry.Provider + "\x00" + entry.Endpoint + "\x00" + entry.Key))
		fingerprint := mac.Sum(nil)
		sealed := s.aead.Seal(nil, nil, []byte(entry.Key), entryAAD(entry))
		result, err := tx.ExecContext(ctx, `
			INSERT INTO credentials (provider, endpoint, label, fingerprint, ciphertext)
			SELECT ?, ?, ?, ?, ?
			WHERE NOT EXISTS (
				SELECT 1 FROM credentials WHERE provider = ? AND endpoint = ? AND fingerprint = ?
			)
			ON CONFLICT (provider, endpoint, label) DO UPDATE
			SET fingerprint = excluded.fingerprint, ciphertext = excluded.ciphertext`,
			entry.Provider, entry.Endpoint, entry.Label, fingerprint, sealed, entry.Provider, entry.Endpoint, fingerprint)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += int(changed)
	}
	return count, tx.Commit()
}

func (s *Store) Entries(ctx context.Context, provider, endpoint string) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT label, ciphertext FROM credentials
		WHERE provider = ? AND endpoint = ? ORDER BY label`, provider, endpoint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		entry := Entry{Provider: provider, Endpoint: endpoint}
		var encrypted []byte
		if err := rows.Scan(&entry.Label, &encrypted); err != nil {
			return nil, err
		}
		key, err := s.aead.Open(nil, nil, encrypted, entryAAD(entry))
		if err != nil {
			return nil, fmt.Errorf("could not decrypt credential %q for %q", entry.Label, provider)
		}
		entry.Key = string(key)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) Remove(ctx context.Context, provider, endpoint, label string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM credentials
		WHERE provider = ? AND endpoint = ? AND label = ?`, provider, endpoint, label)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *Store) Cooldown(ctx context.Context, provider, endpoint string) (time.Time, error) {
	var until int64
	err := s.db.QueryRowContext(ctx, `SELECT until_unix_ms FROM cooldowns WHERE provider = ? AND endpoint = ?`,
		provider, endpoint).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	return time.UnixMilli(until), err
}

func (s *Store) SetCooldown(ctx context.Context, provider, endpoint string, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO cooldowns VALUES (?, ?, ?)
		ON CONFLICT (provider, endpoint) DO UPDATE
		SET until_unix_ms = MAX(until_unix_ms, excluded.until_unix_ms)`,
		provider, endpoint, until.Add(time.Millisecond-1).UnixMilli())
	return err
}

// SaveResponse stores one encrypted Responses turn list.
func (s *Store) SaveResponse(ctx context.Context, id string, turns []byte, expires time.Time) error {
	if id == "" || len(turns) == 0 {
		return nil
	}
	sealed := s.aead.Seal(nil, nil, turns, responseAAD(id))
	now := time.Now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO response_turns (id, ciphertext, expires_unix_ms) VALUES (?, ?, ?)
		ON CONFLICT (id) DO UPDATE
		SET ciphertext = excluded.ciphertext, expires_unix_ms = excluded.expires_unix_ms`,
		id, sealed, expires.UnixMilli()); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM response_turns WHERE expires_unix_ms <= ?`, now.UnixMilli()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM response_turns WHERE id NOT IN (
			SELECT id FROM response_turns ORDER BY expires_unix_ms DESC LIMIT 128
		)`)
	return err
}

// LoadResponse reads a stored turn list. A missing or expired id is nil, false.
func (s *Store) LoadResponse(ctx context.Context, id string) ([]byte, bool) {
	if id == "" {
		return nil, false
	}
	var sealed []byte
	var expires int64
	if err := s.db.QueryRowContext(ctx, `SELECT ciphertext, expires_unix_ms FROM response_turns WHERE id = ?`, id).
		Scan(&sealed, &expires); err != nil {
		return nil, false
	}
	if time.Now().UnixMilli() >= expires {
		return nil, false
	}
	turns, err := s.aead.Open(nil, nil, sealed, responseAAD(id))
	if err != nil {
		return nil, false
	}
	return turns, true
}

func responseAAD(id string) []byte {
	return []byte("response/v1\x00" + id)
}

func entryAAD(entry Entry) []byte {
	return []byte("credential/v1\x00" + entry.Provider + "\x00" + entry.Endpoint + "\x00" + entry.Label)
}

func validKey(key string) bool {
	if len(key) == 0 || len(key) > 8192 {
		return false
	}
	for i := range len(key) {
		// Printable ASCII including spaces: web-session credentials are JSON.
		if key[i] < ' ' || key[i] > '~' {
			return false
		}
	}
	return true
}
