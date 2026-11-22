// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

const toolsUsage = `allaiis tools — inspect the tool name record the doctor reads

  allaiis tools show [--json]   Print repaired and unknown name counts
  allaiis tools reset           Forget the record (a running gateway follows)
`

// toolNameRecord mirrors the file the gateway keeps.
type toolNameRecord struct {
	Folded   int64            `json:"folded"`
	Suffixed int64            `json:"suffixed"`
	Unknown  int64            `json:"unknown"`
	Names    map[string]int   `json:"names"`
	Seen     map[string]int64 `json:"seen"`
}

func runTools(dataDir, configPath string, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, toolsUsage)
		return nil
	}
	path := filepath.Join(dataDir, "tool-names.json")
	switch args[0] {
	case "show":
		asJSON := len(args) > 1 && args[1] == "--json"
		return showToolNames(os.Stdout, path, retentionDays(configPath), asJSON)
	case "reset":
		return resetToolNames(os.Stdout, path)
	default:
		return fmt.Errorf("unknown tools command %q", args[0])
	}
}

// retentionDays reads the configured retention, or zero when the config cannot
// be read: without it nothing is called expired.
func retentionDays(configPath string) int {
	cfg, err := config.Load(configPath)
	if err != nil || cfg.ToolNameTTLDays < 0 {
		return 0
	}
	return cfg.ToolNameTTLDays
}

// showToolNames prints the record, or says there is none. A gateway that is not
// running never prunes the file, so the command applies the retention itself
// and marks what would go.
func showToolNames(out io.Writer, path string, ttlDays int, asJSON bool) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "No tool name record yet.")
		return nil
	}
	if err != nil {
		return err
	}
	var record toolNameRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return fmt.Errorf("the record is not readable: %w", err)
	}
	if asJSON {
		return writeJSON(out, record)
	}
	repaired := record.Folded + record.Suffixed
	fmt.Fprintf(out, "Repaired names: %d (folded %d, trailing word %d)\n", repaired, record.Folded, record.Suffixed)
	fmt.Fprintf(out, "Unknown names: %d\n", record.Unknown)
	if len(record.Names) == 0 {
		return nil
	}
	names := make([]string, 0, len(record.Names))
	for name := range record.Names {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if record.Names[names[i]] != record.Names[names[j]] {
			return record.Names[names[i]] > record.Names[names[j]]
		}
		return names[i] < names[j]
	})
	table := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(table, "\nNAME\tCALLS\tLAST SEEN")
	now := time.Now()
	expired := 0
	for _, name := range names {
		shown := age(now, record.Seen[name], ttlDays)
		if shown == expiredLabel {
			expired++
		}
		fmt.Fprintf(table, "%s\t%d\t%s\n", name, record.Names[name], shown)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if expired > 0 {
		fmt.Fprintf(out, "\n%d expired: a gateway drops them on its next start.\n", expired)
	}
	return nil
}

const expiredLabel = "expired"

// age renders how long ago a name was last seen, or that it is past retention.
func age(now time.Time, stamp int64, ttlDays int) string {
	if stamp == 0 {
		return "unknown"
	}
	span := now.Sub(time.UnixMilli(stamp))
	if ttlDays > 0 && span > time.Duration(ttlDays)*24*time.Hour {
		return expiredLabel
	}
	switch {
	case span < time.Minute:
		return "just now"
	case span < time.Hour:
		return fmt.Sprintf("%dm", int(span.Minutes()))
	case span < 24*time.Hour:
		return fmt.Sprintf("%dh", int(span.Hours()))
	}
	return fmt.Sprintf("%dd", int(span.Hours()/24))
}

// resetToolNames removes the record; a running gateway notices the deletion.
func resetToolNames(out io.Writer, path string) error {
	os.Remove(path + ".tmp")
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "No record to clear.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Record cleared. A running gateway drops it within a few seconds.")
	return nil
}
