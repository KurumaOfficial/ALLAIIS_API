// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/catalog"
	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

func runModels(ctx context.Context, path string, args []string) error {
	if len(args) == 0 {
		return errors.New("expected models discover or models add")
	}
	command := args[0]
	flags := flag.NewFlagSet("models "+command, flag.ContinueOnError)
	providerID := flags.String("provider", "", "provider ID")
	var match, model, name, label, privacy string
	switch command {
	case "discover":
		flags.StringVar(&match, "match", "", "filter models by ID or name")
	case "add":
		flags.StringVar(&model, "upstream-model", "", "exact upstream model ID")
		flags.StringVar(&name, "name", "", "alias base name (defaults to the upstream model ID)")
		flags.StringVar(&label, "label", "", "bind this alias to a named credential")
		flags.StringVar(&privacy, "privacy", string(config.Consumer), "privacy classification of the upstream route")
	default:
		return fmt.Errorf("unknown models command %q", command)
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *providerID == "" {
		return errors.New("--provider is required and positional arguments are unsupported")
	}

	switch command {
	case "discover":
		cfg := config.Default()
		provider, exists := cfg.Provider(*providerID)
		if !exists || provider.CatalogURL() == "" {
			return errors.New("public catalog discovery supports venice, opencode and opencode-go")
		}
		models, err := catalog.List(ctx, provider.CatalogURL(), match)
		if err != nil {
			return fmt.Errorf("discover %s models: %w", *providerID, err)
		}
		return writeJSON(os.Stdout, map[string]any{"provider": *providerID, "models": models})
	case "add":
		if model == "" {
			return errors.New("--upstream-model is required")
		}
		if name == "" {
			name = model
		}
		route := config.Route{
			ID: name + "-" + *providerID, Provider: *providerID, UpstreamModel: model,
			Privacy: config.Privacy(privacy), CredentialLabel: label,
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
