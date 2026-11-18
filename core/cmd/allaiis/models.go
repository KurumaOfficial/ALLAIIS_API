// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func runModels(ctx context.Context, path string, args []string) error {
	if len(args) == 0 {
		return errors.New("expected models discover or models add")
	}
	command := args[0]
	flags := flag.NewFlagSet("models "+command, flag.ContinueOnError)
	providerID := flags.String("provider", "", "provider ID")
	var match, model, name, label, privacy, protocol string
	var all, withKeys bool
	switch command {
	case "discover":
		flags.StringVar(&match, "match", "", "filter models by ID or name")
		flags.BoolVar(&all, "all", false, "check all built-in and configured providers")
		flags.BoolVar(&withKeys, "with-keys", false, "use stored API keys where available")
		flags.StringVar(&label, "label", "", "use a specific stored credential (single provider)")
	case "add":
		flags.StringVar(&model, "upstream-model", "", "exact upstream model ID")
		flags.StringVar(&name, "name", "", "alias base name (defaults to the upstream model ID)")
		flags.StringVar(&label, "label", "", "bind this alias to a named credential")
		flags.StringVar(&privacy, "privacy", string(config.Consumer), "privacy classification of the upstream route")
		flags.StringVar(&protocol, "protocol", "chat", "route protocol: chat, responses, or messages")
	default:
		return fmt.Errorf("unknown models command %q", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}

	switch command {
	case "discover":
		if all == (*providerID != "") || (all && label != "") {
			return errors.New("use --all or --provider; --label requires a single provider")
		}
		return discoverModels(ctx, path, *providerID, match, label, withKeys)
	case "add":
		if *providerID == "" || model == "" {
			return errors.New("--provider and --upstream-model are required")
		}
		if name == "" {
			name = model
		}
		route := config.Route{
			ID: name + "-" + *providerID, Provider: *providerID, UpstreamModel: model,
			Privacy: config.Privacy(privacy), CredentialLabel: label, Protocol: config.Protocol(protocol),
		}
		added, err := config.AddModel(ctx, path, route)
		if err != nil {
			return fmt.Errorf("edit model configuration: %w", err)
		}
		if added {
			fmt.Fprintf(os.Stdout, "Added %s. Restart a running gateway to load the model.\n", route.ID)
		} else {
			fmt.Fprintf(os.Stdout, "Model %s is already configured.\n", route.ID)
		}
	}
	return nil
}
