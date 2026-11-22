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
)

const toolsUsage = `allaiis tools — inspect the tool name record the doctor reads

  allaiis tools show [--json]   Print repaired and unknown name counts
  allaiis tools reset           Forget the record (a running gateway follows)
`

// toolNameRecord mirrors the file the gateway keeps.
type toolNameRecord struct {
	Folded   int64          `json:"folded"`
	Suffixed int64          `json:"suffixed"`
	Unknown  int64          `json:"unknown"`
	Names    map[string]int `json:"names"`
}

func runTools(dataDir string, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stdout, toolsUsage)
		return nil
	}
	path := filepath.Join(dataDir, "tool-names.json")
	switch args[0] {
	case "show":
		asJSON := len(args) > 1 && args[1] == "--json"
		return showToolNames(os.Stdout, path, asJSON)
	case "reset":
		return resetToolNames(os.Stdout, path)
	default:
		return fmt.Errorf("unknown tools command %q", args[0])
	}
}

// showToolNames prints the record, or says there is none.
func showToolNames(out io.Writer, path string, asJSON bool) error {
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
	fmt.Fprintln(table, "\nNAME\tCALLS")
	for _, name := range names {
		fmt.Fprintf(table, "%s\t%d\n", name, record.Names[name])
	}
	return table.Flush()
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
