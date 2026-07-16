package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/AadiJo/turnal/internal/adapterplugin"
	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/primitives"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
	"github.com/spf13/cobra"
)

const adapterTimeout = 5 * time.Second

func adapterCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "adapter", Short: "Discover and inspect external agent adapters"}
	cmd.AddCommand(adapterListCmd(), adapterDoctorCmd(), adapterCaptureCmd())
	return cmd
}

func adapterListCmd() *cobra.Command {
	return &cobra.Command{
		Use: "list", Short: "List turnal-adapter-* executables", SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			discovered, err := adapterplugin.Discover()
			if err != nil {
				return err
			}
			if len(discovered) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no external adapters found")
				return nil
			}
			for _, external := range discovered {
				ctx, cancel := context.WithTimeout(cmd.Context(), adapterTimeout)
				inspection := adapterplugin.Inspect(ctx, external)
				cancel()
				if inspection.Err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\tinvalid\t%s\t%s\n", external.Name, inspection.Err, external.Path)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tprotocol v%d\t%s\n", inspection.Manifest.Name, inspection.Manifest.AdapterVersion, inspection.Manifest.ProtocolVersions[0], external.Path)
			}
			return nil
		},
	}
}

func adapterDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use: "doctor [adapter...]", Short: "Check adapter discovery and protocol compatibility", SilenceUsage: true, Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, names []string) error {
			var discovered []adapterplugin.Adapter
			if len(names) == 0 {
				var err error
				discovered, err = adapterplugin.Discover()
				if err != nil {
					return err
				}
				if len(discovered) == 0 {
					return fmt.Errorf("no external adapters found on PATH")
				}
			} else {
				for _, name := range names {
					if _, err := primitives.ParseAdapterName(name); err != nil {
						return err
					}
					external, err := adapterplugin.Find(name)
					if err != nil {
						return err
					}
					discovered = append(discovered, external)
				}
			}
			failures := 0
			for _, external := range discovered {
				ctx, cancel := context.WithTimeout(cmd.Context(), adapterTimeout)
				inspection := adapterplugin.Inspect(ctx, external)
				cancel()
				if inspection.Err != nil {
					failures++
					fmt.Fprintf(cmd.OutOrStdout(), "%s: failed (%v)\n", external.Name, inspection.Err)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: ok (adapter %s, protocol v%d, %s)\n", inspection.Manifest.Name, inspection.Manifest.AdapterVersion, inspection.Manifest.ProtocolVersions[0], inspection.Duration.Round(time.Millisecond))
			}
			if failures > 0 {
				return fmt.Errorf("%d adapter check(s) failed", failures)
			}
			return nil
		},
	}
}

func adapterCaptureCmd() *cobra.Command {
	return &cobra.Command{
		Use: "capture <adapter> <hook>", Short: "Internal: normalize and capture an external provider hook", Hidden: true, SilenceUsage: true, Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readAdapterPayload(cmd.InOrStdin())
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "turnal: %s adapter capture failed (%s): %v\n", args[0], args[1], err)
				return nil
			}
			name, err := primitives.ParseAdapterName(args[0])
			if err == nil {
				var external adapterplugin.Adapter
				external, err = adapterplugin.Find(name.String())
				if err == nil {
					ctx, cancel := context.WithTimeout(cmd.Context(), adapterTimeout)
					var normalized []adaptersdk.Event
					normalized, err = adapterplugin.Normalize(ctx, external, args[1], raw)
					cancel()
					if err == nil {
						err = adapters.HandleNormalizedEvents(name, args[1], raw, normalized)
					}
				}
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "turnal: %s adapter capture failed (%s): %v\n", args[0], args[1], err)
			}
			// Gemini and other command-hook providers expect valid JSON output.
			fmt.Fprintln(cmd.OutOrStdout(), "{}")
			return nil
		},
	}
}

func readAdapterPayload(reader io.Reader) (json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, adapters.MaxHookPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read adapter payload: %w", err)
	}
	if len(raw) > adapters.MaxHookPayloadBytes {
		return nil, fmt.Errorf("adapter payload exceeds %d-byte limit", adapters.MaxHookPayloadBytes)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("adapter payload must be valid JSON")
	}
	return raw, nil
}
