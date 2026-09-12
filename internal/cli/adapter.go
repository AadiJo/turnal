package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/AadiJo/turnal/internal/adapterplugin"
	"github.com/AadiJo/turnal/internal/adapters"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/runs"
	adaptersdk "github.com/AadiJo/turnal/sdk/adapter"
	"github.com/spf13/cobra"
)

const adapterTimeout = 5 * time.Second

func adapterCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "adapter", Short: "Discover, inspect, and build external agent adapters"}
	cmd.AddCommand(adapterContractCmd(), adapterListCmd(), adapterDoctorCmd(), adapterCaptureCmd())
	return cmd
}

const adapterContractSchemaVersion = 1

type adapterContract struct {
	SchemaVersion     int                            `json:"schema_version"`
	Protocol          string                         `json:"protocol"`
	ProtocolVersion   int                            `json:"protocol_version"`
	Transport         string                         `json:"transport"`
	ExecutablePattern string                         `json:"executable_pattern"`
	GoPackage         string                         `json:"go_package"`
	EventTypes        []string                       `json:"event_types"`
	SessionTopology   adapterSessionTopologyContract `json:"session_topology"`
	Documentation     string                         `json:"documentation"`
}

type adapterSessionTopologyContract struct {
	EventType          string `json:"event_type"`
	ParentSessionField string `json:"parent_session_field"`
	ParentToolField    string `json:"parent_tool_field"`
}

func currentAdapterContract() adapterContract {
	return adapterContract{
		SchemaVersion:     adapterContractSchemaVersion,
		Protocol:          adaptersdk.ProtocolName,
		ProtocolVersion:   adaptersdk.ProtocolVersion,
		Transport:         "NDJSON over stdin/stdout",
		ExecutablePattern: "turnal-adapter-<name>",
		GoPackage:         "github.com/AadiJo/turnal/sdk/adapter",
		EventTypes: []string{
			string(adaptersdk.EventSessionStart),
			string(adaptersdk.EventPromptUser),
			string(adaptersdk.EventToolCall),
			string(adaptersdk.EventToolResult),
			string(adaptersdk.EventAssistantMessage),
			string(adaptersdk.EventTurnFinish),
		},
		SessionTopology: adapterSessionTopologyContract{
			EventType:          string(adaptersdk.EventSessionStart),
			ParentSessionField: "parent_session_id",
			ParentToolField:    "parent_tool_use_id",
		},
		Documentation: "https://github.com/AadiJo/turnal/blob/main/docs/adapters.md",
	}
}

func adapterContractCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use: "contract", Short: "Describe the external adapter plugin contract", SilenceUsage: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			contract := currentAdapterContract()
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(contract)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "protocol: %s v%d\nexecutable: %s\ntransport: %s\nGo SDK: %s\nsession topology: %s fields %s and %s\ndocs: %s\n",
				contract.Protocol, contract.ProtocolVersion, contract.ExecutablePattern, contract.Transport,
				contract.GoPackage, contract.SessionTopology.EventType, contract.SessionTopology.ParentSessionField,
				contract.SessionTopology.ParentToolField, contract.Documentation)
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit structured JSON")
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
			var normalized []adaptersdk.Event
			if err == nil {
				var external adapterplugin.Adapter
				external, err = adapterplugin.Find(name.String())
				if err == nil {
					ctx, cancel := context.WithTimeout(cmd.Context(), adapterTimeout)
					normalized, err = adapterplugin.Normalize(ctx, external, args[1], raw)
					cancel()
					if err == nil {
						err = adapters.HandleNormalizedEventsWithRunID(name, args[1], raw, normalized, os.Getenv(runs.EnvRunID))
					}
				}
			}
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "turnal: %s adapter capture failed (%s): %v\n", args[0], args[1], err)
			}
			writeAdapterCaptureOutput(cmd.OutOrStdout(), name, normalized)
			return nil
		},
	}
}

type adapterCaptureOutput struct {
	Continue          *bool  `json:"continue,omitempty"`
	UserMessage       string `json:"user_message,omitempty"`
	AdditionalContext string `json:"additional_context,omitempty"`
}

func writeAdapterCaptureOutput(writer io.Writer, name primitives.AdapterName, normalized []adaptersdk.Event) {
	for _, event := range normalized {
		if event.Type != adaptersdk.EventPromptUser {
			continue
		}
		instruction, ok := adapters.IntentInstructionForSession(event.CWD, event.SessionID)
		if !ok {
			break
		}
		output := adapterCaptureOutput{}
		switch name {
		case primitives.AdapterCursor:
			proceed := true
			output.Continue = &proceed
			output.UserMessage = event.Text + "\n\n" + instruction
		case primitives.AdapterPi:
			output.AdditionalContext = instruction
		default:
			fmt.Fprintln(writer, "{}")
			return
		}
		_ = json.NewEncoder(writer).Encode(output)
		return
	}
	fmt.Fprintln(writer, "{}")
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
