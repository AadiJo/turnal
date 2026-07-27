package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/AadiJo/turnal/internal/buildinfo"
	"github.com/AadiJo/turnal/internal/upgrade"
)

type cliFakeRegistry struct {
	tags map[string]string
}

func (r cliFakeRegistry) DistTags(ctx context.Context) (map[string]string, error) {
	return r.tags, nil
}

func (r cliFakeRegistry) Version(ctx context.Context, npmTag string) (string, error) {
	return "", fmt.Errorf("missing fake npm tag %q", npmTag)
}

func TestUpgradeDryRunJSON(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.1", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceNPM)
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"latest": "0.4.2"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("run command called during dry run: %#v", command)
		return nil
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"upgrade", "--dry-run", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("upgrade --dry-run --json: %v", err)
	}

	var plan upgrade.Plan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, out.String())
	}
	if plan.Target.NPMTag != "latest" || plan.Target.Version != "0.4.2" {
		t.Fatalf("target = %+v", plan.Target)
	}
	if !reflect.DeepEqual(plan.Action.Command, []string{"npm", "install", "-g", "@aadijo/turnal@latest"}) {
		t.Fatalf("command = %#v", plan.Action.Command)
	}
}

func TestUpgradeRunsNPMCommand(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.1", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceNPM)
	var ran []string
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"latest": "0.4.2"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		ran = append([]string(nil), command...)
		_, _ = fmt.Fprintln(stdout, "npm ran")
		return nil
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"upgrade"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out.String())
	}

	if !reflect.DeepEqual(ran, []string{"npm", "install", "-g", "@aadijo/turnal@latest"}) {
		t.Fatalf("ran = %#v", ran)
	}
	if !strings.Contains(out.String(), "action:  npm install -g @aadijo/turnal@latest") {
		t.Fatalf("output missing action:\n%s", out.String())
	}
}

func TestUpgradeRunsStandaloneReplacement(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.1", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceStandalone)
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"latest": "0.4.2"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("npm command called for standalone upgrade: %#v", command)
		return nil
	})
	oldStandaloneRunner := runStandaloneUpgrade
	var installed upgrade.StandaloneInstallOptions
	runStandaloneUpgrade = func(ctx context.Context, opts upgrade.StandaloneInstallOptions) error {
		installed = opts
		return nil
	}
	t.Cleanup(func() {
		runStandaloneUpgrade = oldStandaloneRunner
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"upgrade"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out.String())
	}

	if installed.Version != "0.4.2" || installed.Channel != upgrade.ChannelStable {
		t.Fatalf("standalone install options = %+v", installed)
	}
	if !strings.Contains(out.String(), "action:  download and replace standalone release binaries") {
		t.Fatalf("output missing standalone action:\n%s", out.String())
	}
}

func TestUpgradeNightlySwitchRequiresConfirmationNonInteractive(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceNPM)
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"nightly": "0.4.3-nightly.20260709.4"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("run command called without confirmation: %#v", command)
		return nil
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"upgrade", "--nightly"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("upgrade --nightly succeeded, want confirmation error")
	}
	code, ok := commandExitCode(err)
	if !ok || code != 4 {
		t.Fatalf("exit code = %d %v, want 4 true; err=%v", code, ok, err)
	}
	output := out.String()
	for _, want := range []string{
		"This switches from stable to nightly.",
		"Nightly builds may be less stable than releases.",
		"Run again with --yes to continue.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestUpgradeUnknownInstallSourcePrintsManualInstructionsWithYes(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "", upgrade.InstallSourceUnknown)
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"latest": "0.4.3"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("run command called for unknown install source: %#v", command)
		return nil
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"upgrade", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("upgrade --yes: %v\n%s", err, out.String())
	}

	output := out.String()
	for _, want := range []string{
		"Turnal was not installed through a known package manager.",
		"Update manually:",
		"npm install -g @aadijo/turnal@latest",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestUpgradeCheckExitCodeJSON(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.1", upgrade.ChannelStable, "", upgrade.InstallSourceNPM)
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"latest": "0.4.2"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("run command called during check: %#v", command)
		return nil
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"upgrade", "--check", "--exit-code", "--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("upgrade --check --exit-code --json succeeded, want exit code 3")
	}
	code, ok := commandExitCode(err)
	if !ok || code != 3 {
		t.Fatalf("exit code = %d %v, want 3 true", code, ok)
	}

	var plan upgrade.Plan
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatalf("decode plan: %v\n%s", err, out.String())
	}
	if !plan.UpdateAvailable {
		t.Fatal("UpdateAvailable = false, want true")
	}
}

func TestUpdateAlias(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.1", upgrade.ChannelStable, "", upgrade.InstallSourceNPM)
	setUpgradeTestHooks(t, cliFakeRegistry{tags: map[string]string{"latest": "0.4.2"}}, func(ctx context.Context, command []string, stdout io.Writer, stderr io.Writer) error {
		t.Fatalf("run command called during dry run: %#v", command)
		return nil
	})

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"update", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("update --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "Turnal upgrade") {
		t.Fatalf("alias output missing upgrade header:\n%s", out.String())
	}
}

func TestVersionJSONIncludesBuildMetadata(t *testing.T) {
	setBuildMetadataForTest(t, "0.4.2", upgrade.ChannelStable, "abc1234", upgrade.InstallSourceNPM)

	cmd := NewRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"version", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version --json: %v", err)
	}

	var metadata upgrade.Metadata
	if err := json.Unmarshal(out.Bytes(), &metadata); err != nil {
		t.Fatalf("decode metadata: %v\n%s", err, out.String())
	}
	if metadata.Version != "0.4.2" || metadata.Channel != upgrade.ChannelStable || metadata.Commit != "abc1234" || metadata.InstallSource != upgrade.InstallSourceNPM {
		t.Fatalf("metadata = %+v", metadata)
	}
}

func setBuildMetadataForTest(t *testing.T, testVersion, testChannel, testCommit, testInstallSource string) {
	t.Helper()
	oldVersion := buildinfo.Version
	oldChannel := buildinfo.Channel
	oldCommit := buildinfo.Commit
	oldInstallSource := buildinfo.InstallSource
	buildinfo.Version = testVersion
	buildinfo.Channel = testChannel
	buildinfo.Commit = testCommit
	buildinfo.InstallSource = testInstallSource
	t.Cleanup(func() {
		buildinfo.Version = oldVersion
		buildinfo.Channel = oldChannel
		buildinfo.Commit = oldCommit
		buildinfo.InstallSource = oldInstallSource
	})
}

func setUpgradeTestHooks(t *testing.T, registry upgrade.Registry, runner func(context.Context, []string, io.Writer, io.Writer) error) {
	t.Helper()
	oldRegistry := newUpgradeRegistry
	oldRunner := runUpgradeCommand
	newUpgradeRegistry = func(upgrade.Metadata) upgrade.Registry {
		return registry
	}
	runUpgradeCommand = runner
	t.Cleanup(func() {
		newUpgradeRegistry = oldRegistry
		runUpgradeCommand = oldRunner
	})
}
