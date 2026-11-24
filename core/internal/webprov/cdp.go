// SPDX-License-Identifier: GPL-3.0-only

package webprov

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/net/websocket"
)

// cdpEndpoint is the loopback DevTools address of the capture browser.
const cdpEndpoint = "http://127.0.0.1:9222"

var chromeCandidates = []string{
	`C:\Program Files\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
	`C:\Program Files\BraveSoftware\Brave-Browser\Application\brave.exe`,
	`/usr/bin/google-chrome`,
	`/usr/bin/microsoft-edge`,
	`/usr/bin/brave-browser`,
	`/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,
	`/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge`,
	`/Applications/Brave Browser.app/Contents/MacOS/Brave Browser`,
}

var chromeOverride = ""

// SetChromePath overrides the auto-detected Chrome executable.
func SetChromePath(path string) { chromeOverride = path }

// NewDistinctID returns a random browser-style device identifier.
func NewDistinctID() string { return randomUUID() }

// ErrCDP reports that no Chrome with a DevTools port could be reached.
var ErrCDP = errors.New("no Chrome DevTools endpoint on 127.0.0.1:9222; run `allaiis web chrome` first")

// LaunchChrome starts Chrome with a dedicated profile and a DevTools port. The
// profile path is stable, so the login survives restarts. That matters because
// Chrome binds its cookie encryption key to the profile directory.
func LaunchChrome(profileDir, url string) error {
	binary := chromeOverride
	for _, candidate := range chromeCandidates {
		if _, err := os.Stat(candidate); err == nil {
			binary = candidate
			break
		}
	}
	if binary == "" {
		if found, err := exec.LookPath("chrome"); err == nil {
			binary = found
		}
	}
	if binary == "" {
		return errors.New("no Chromium browser was found (Chrome, Edge or Brave); install one or pass --chrome")
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return err
	}
	args := []string{
		"--remote-debugging-port=9222",
		"--user-data-dir=" + profileDir,
		"--no-first-run", "--no-default-browser-check",
	}
	if url != "" {
		args = append(args, url)
	}
	return exec.Command(binary, args...).Start()
}

type browserCookie struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
}

type cdpSession struct {
	conn *websocket.Conn
	id   int
}

func dialCDP(ctx context.Context) (*cdpSession, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, cdpEndpoint+"/json/list", nil)
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrCDP
	}
	defer response.Body.Close()
	var targets []struct {
		Type                 string `json:"type"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&targets); err != nil {
		return nil, err
	}
	wsURL := ""
	for _, target := range targets {
		if target.Type == "page" && target.WebSocketDebuggerURL != "" {
			wsURL = target.WebSocketDebuggerURL
			break
		}
	}
	if wsURL == "" {
		return nil, errors.New("chrome has no open page; open https://venice.ai")
	}
	conn, err := websocket.Dial(wsURL, "", cdpEndpoint)
	if err != nil {
		return nil, err
	}
	return &cdpSession{conn: conn}, nil
}

func (s *cdpSession) Close() { s.conn.Close() }

func (s *cdpSession) call(method string, result any) error {
	s.id++
	if err := websocket.JSON.Send(s.conn, map[string]any{"id": s.id, "method": method}); err != nil {
		return err
	}
	for {
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := websocket.JSON.Receive(s.conn, &reply); err != nil {
			return err
		}
		if reply.ID != s.id {
			continue
		}
		if reply.Error != nil {
			return errors.New(reply.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(reply.Result, result)
		}
		return nil
	}
}

// Capture reads the Venice cookies and User-Agent from a logged-in Chrome tab.
func Capture(ctx context.Context) (Credential, error) {
	session, err := dialCDP(ctx)
	if err != nil {
		return Credential{}, err
	}
	defer session.Close()
	var version struct {
		UserAgent string `json:"userAgent"`
	}
	if err := session.call("Browser.getVersion", &version); err != nil {
		return Credential{}, err
	}
	var cookies struct {
		Cookies []browserCookie `json:"cookies"`
	}
	if err := session.call("Storage.getCookies", &cookies); err != nil {
		return Credential{}, err
	}
	cred := Credential{UserAgent: version.UserAgent, DistinctID: randomUUID()}
	clearance := false
	for _, cookie := range cookies.Cookies {
		switch cookie.Domain {
		case ".venice.ai", "venice.ai":
			cred.Venice = append(cred.Venice, Cookie{Name: cookie.Name, Value: cookie.Value})
			clearance = clearance || cookie.Name == "cf_clearance"
		case ".clerk.venice.ai", "clerk.venice.ai":
			cred.Clerk = append(cred.Clerk, Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}
	sort.Slice(cred.Venice, func(i, j int) bool { return cred.Venice[i].Name < cred.Venice[j].Name })
	sort.Slice(cred.Clerk, func(i, j int) bool { return cred.Clerk[i].Name < cred.Clerk[j].Name })
	if !clearance {
		return cred, errors.New("no venice.ai cf_clearance cookie; open https://venice.ai and wait for the check")
	}
	if cred.UserAgent == "" {
		cred.UserAgent = DefaultUserAgent
	}
	return cred, nil
}

// CaptureFromHeader parses a pasted `Cookie:` header instead of using Chrome.
func CaptureFromHeader(header string) (Credential, error) {
	cred := Credential{UserAgent: DefaultUserAgent}
	for _, part := range splitCookies(header) {
		name, value, found := cut(part, '=')
		if !found || name == "" {
			continue
		}
		switch name {
		case "cf_clearance", "__cf_bm":
			cred.Venice = append(cred.Venice, Cookie{Name: name, Value: value})
		case "__client":
			cred.Clerk = append(cred.Clerk, Cookie{Name: name, Value: value})
		}
	}
	if len(cred.Venice) == 0 {
		return cred, errors.New("the pasted header needs at least a cf_clearance cookie")
	}
	return cred, nil
}

func splitCookies(header string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(header); i++ {
		if header[i] != ';' {
			continue
		}
		parts = append(parts, trimSpace(header[start:i]))
		start = i + 1
	}
	return append(parts, trimSpace(header[start:]))
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func cut(s string, sep byte) (string, string, bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return trimSpace(s[:i]), trimSpace(s[i+1:]), true
		}
	}
	return s, "", false
}

// DefaultProfileDir is the capture profile inside the data directory.
func DefaultProfileDir(dataDir string) string { return filepath.Join(dataDir, "chrome-profile") }

func randomUUID() string {
	buf := make([]byte, 16)
	rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
