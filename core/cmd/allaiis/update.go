// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const updateUsage = `allaiis update — replace this binary with the latest release

  allaiis update --check    Only report the latest tag
  allaiis update            Download, verify the checksum and swap the binary
`

const releaseRepo = "KurumaOfficial/ALLAIIS_API"

// release is the part of the GitHub answer update needs.
type release struct {
	Tag    string `json:"tag_name"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// runUpdate swaps the executable for the newest release.
func runUpdate(ctx context.Context, args []string) error {
	check := false
	force := false
	for _, arg := range args {
		switch arg {
		case "--check":
			check = true
		case "--force":
			force = true
		case "help", "--help":
			fmt.Fprint(os.Stdout, updateUsage)
			return nil
		default:
			return fmt.Errorf("unknown update argument %q", arg)
		}
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	latest, err := latestRelease(ctx, client)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Latest release: %s (this binary: %s)\n", latest.Tag, version)
	if check || (latest.Tag == "v"+version && !force) {
		return nil
	}
	suffix := runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		suffix += ".exe"
	}
	asset, sums := "", ""
	for _, item := range latest.Assets {
		switch {
		case strings.HasSuffix(item.Name, suffix):
			asset = item.URL
		case item.Name == "SHA256SUMS":
			sums = item.URL
		}
	}
	if asset == "" || sums == "" {
		return fmt.Errorf("the release lacks a build for %s/%s or its checksums", runtime.GOOS, runtime.GOARCH)
	}
	want, err := wantedSum(ctx, client, sums, filepath.Base(asset))
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	fresh := executable + ".new"
	if err := download(ctx, client, asset, fresh); err != nil {
		return err
	}
	got, err := fileSum(fresh)
	if err != nil {
		return err
	}
	if got != want {
		os.Remove(fresh)
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return swapBinary(executable, fresh)
}

// latestRelease asks GitHub for the newest tag.
func latestRelease(ctx context.Context, client *http.Client) (release, error) {
	var found release
	url := "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return found, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "ALLAIIS/"+version)
	// A private repository needs a token; the public one does not.
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return found, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound:
		return found, fmt.Errorf("no releases published yet; push a v* tag to make one")
	case http.StatusUnauthorized, http.StatusForbidden:
		return found, fmt.Errorf("GitHub refused (%d); a private repository needs GITHUB_TOKEN", response.StatusCode)
	case http.StatusOK:
	default:
		return found, fmt.Errorf("GitHub answered %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&found); err != nil {
		return found, err
	}
	if found.Tag == "" {
		return found, fmt.Errorf("the release has no tag")
	}
	return found, nil
}

// wantedSum finds the hash of one file in the checksum list.
func wantedSum(ctx context.Context, client *http.Client, url, name string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("%s is not listed in SHA256SUMS", name)
}

// download writes a URL to a file.
func download(ctx context.Context, client *http.Client, url, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download answered %d", response.StatusCode)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, io.LimitReader(response.Body, 256<<20))
	return err
}

func fileSum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// swapBinary moves the new file into place. Windows allows renaming a running
// executable, so the old one is renamed aside first.
func swapBinary(executable, fresh string) error {
	old := executable + ".old"
	os.Remove(old)
	if err := os.Rename(executable, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, executable); err != nil {
		os.Rename(old, executable)
		return err
	}
	fmt.Fprintf(os.Stdout, "Updated %s. Restart the gateway to use it.\n", executable)
	return nil
}
