// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/KurumaOfficial/ALLAIIS_API/core/internal/config"
)

// providerWeb reports whether a provider uses a web transport.
func providerWeb(ctx context.Context, path, id string) bool {
	_ = ctx
	cfg, err := config.Load(path)
	if err != nil {
		return false
	}
	provider, ok := cfg.Provider(id)
	return ok && provider.Web()
}

// triState parses an optional boolean flag.
func triState(raw string) (*bool, error) {
	if raw == "" {
		return nil, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return nil, errors.New("--reasoning must be true or false")
	}
	return &value, nil
}

func runModels(ctx context.Context, path string, args []string) error {
	if len(args) == 0 {
		return errors.New("expected models discover or models add")
	}
	command := args[0]
	flags := flag.NewFlagSet("models "+command, flag.ContinueOnError)
	providerID := flags.String("provider", "", "provider ID")
	var match, model, name, label, privacy, protocol, reasoning string
	var maxContext, maxResponse, maxImages, maxTools, inactivity, maxVideos int
	var webSearch, largeContext, strictOrder, roundTrip, minP, e2ee bool
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
		flags.StringVar(&reasoning, "reasoning", "", "true or false: force thinking for this model")
		flags.IntVar(&maxContext, "max-context", 0, "model context window in tokens")
		flags.IntVar(&maxResponse, "max-response", 0, "maximum response tokens")
		flags.IntVar(&maxImages, "max-images", 0, "maximum attachments per request")
		flags.IntVar(&maxTools, "max-tools", 0, "maximum tools per request")
		flags.IntVar(&maxVideos, "max-videos", 0, "maximum videos per request")
		flags.BoolVar(&webSearch, "web-search", false, "enable the provider's web tool")
		flags.BoolVar(&largeContext, "large-context", false, "ask for the extended-context mode")
		flags.BoolVar(&strictOrder, "strict-order", false, "merge consecutive turns for strict models")
		flags.BoolVar(&e2ee, "e2ee", false, "encrypt messages to the model's verified enclave")
		flags.IntVar(&inactivity, "inactivity-ms", 0, "abort a stream that stays silent this long")
		flags.BoolVar(&roundTrip, "reasoning-round-trip", false, "replay stored thinking with follow-ups")
		flags.BoolVar(&minP, "min-p", false, "forward min_p to models that support it")
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
		thinking, err := triState(reasoning)
		if err != nil {
			return err
		}
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		provider, ok := cfg.Provider(*providerID)
		if !ok {
			return fmt.Errorf("unknown provider %q", *providerID)
		}
		route := config.Route{
			ID:       config.DefaultAliasFor(*providerID, name, provider.Transport == config.TransportVeniceWeb),
			Provider: *providerID, UpstreamModel: model,
			Privacy: config.Privacy(privacy), CredentialLabel: label, Protocol: config.Protocol(protocol),
			Reasoning: thinking, MaxContext: maxContext, MaxResponse: maxResponse, MaxImages: maxImages,
			MaxTools: maxTools, WebSearch: webSearch, LargeContext: largeContext,
			StrictOrder: strictOrder, InactivityMS: inactivity, ReasoningRoundTrip: roundTrip, MaxVideos: maxVideos,
			MinP: minP, E2EE: e2ee || strings.HasPrefix(model, "e2ee-"),
		}
		added, err := config.AddModel(ctx, path, route)
		if err != nil {
			return fmt.Errorf("edit model configuration: %w", err)
		}
		if added {
			fmt.Fprintf(os.Stdout, "Added %s. A running gateway picks it up within seconds.\n", route.ID)
			if label == "" && providerWeb(ctx, path, *providerID) {
				fmt.Fprintln(os.Stdout, "Hint: pass --label to pin the alias when the provider has several sessions.")
			}
		} else {
			fmt.Fprintf(os.Stdout, "Model %s is already configured.\n", route.ID)
		}
	}
	return nil
}
