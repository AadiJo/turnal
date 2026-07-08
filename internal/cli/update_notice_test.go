package cli

import (
	"testing"

	"github.com/AadiJo/turnal/internal/upgrade"
	"github.com/spf13/cobra"
)

func TestShouldShowUpdateNoticeAllowsInteractiveNPMReleaseCommand(t *testing.T) {
	withoutUpdateNoticeEnv(t)
	withUpdateNoticeDisplay(t, true)
	root := &cobra.Command{Use: "turnal"}
	status := &cobra.Command{Use: "status"}
	root.AddCommand(status)

	if !shouldShowUpdateNotice(status, updateNoticeTestMetadata()) {
		t.Fatal("shouldShowUpdateNotice = false, want true")
	}
}

func TestShouldShowUpdateNoticeSkipsJSONCommand(t *testing.T) {
	withoutUpdateNoticeEnv(t)
	withUpdateNoticeDisplay(t, true)
	root := &cobra.Command{Use: "turnal"}
	show := &cobra.Command{Use: "show"}
	show.Flags().Bool("json", false, "Emit structured JSON")
	root.AddCommand(show)
	if err := show.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json flag: %v", err)
	}

	if shouldShowUpdateNotice(show, updateNoticeTestMetadata()) {
		t.Fatal("shouldShowUpdateNotice = true, want false")
	}
}

func TestShouldShowUpdateNoticeSkipsUpgradeVersionAndHiddenCommands(t *testing.T) {
	withoutUpdateNoticeEnv(t)
	withUpdateNoticeDisplay(t, true)
	root := &cobra.Command{Use: "turnal"}
	upgradeCmd := &cobra.Command{Use: "upgrade"}
	versionCmd := &cobra.Command{Use: "version"}
	hiddenCmd := &cobra.Command{Use: "turn", Hidden: true}
	recallCmd := &cobra.Command{Use: "recall"}
	hiddenCmd.AddCommand(recallCmd)
	root.AddCommand(upgradeCmd, versionCmd, hiddenCmd)

	for _, cmd := range []*cobra.Command{upgradeCmd, versionCmd, recallCmd} {
		if shouldShowUpdateNotice(cmd, updateNoticeTestMetadata()) {
			t.Fatalf("shouldShowUpdateNotice(%s) = true, want false", cmd.CommandPath())
		}
	}
}

func TestShouldShowUpdateNoticeSkipsCIAndOptOut(t *testing.T) {
	withUpdateNoticeDisplay(t, true)
	root := &cobra.Command{Use: "turnal"}
	status := &cobra.Command{Use: "status"}
	root.AddCommand(status)

	t.Setenv("CI", "true")
	if shouldShowUpdateNotice(status, updateNoticeTestMetadata()) {
		t.Fatal("shouldShowUpdateNotice with CI = true, want false")
	}
	t.Setenv("CI", "")
	t.Setenv("TURNAL_NO_UPDATE_CHECK", "1")
	if shouldShowUpdateNotice(status, updateNoticeTestMetadata()) {
		t.Fatal("shouldShowUpdateNotice with TURNAL_NO_UPDATE_CHECK = true, want false")
	}
}

func TestShouldShowUpdateNoticeSkipsNonNPMOrDevBuilds(t *testing.T) {
	withoutUpdateNoticeEnv(t)
	withUpdateNoticeDisplay(t, true)
	root := &cobra.Command{Use: "turnal"}
	status := &cobra.Command{Use: "status"}
	root.AddCommand(status)

	metadata := updateNoticeTestMetadata()
	metadata.InstallSource = upgrade.InstallSourceUnknown
	if shouldShowUpdateNotice(status, metadata) {
		t.Fatal("shouldShowUpdateNotice for unknown install source = true, want false")
	}

	metadata = updateNoticeTestMetadata()
	metadata.Channel = upgrade.ChannelDev
	if shouldShowUpdateNotice(status, metadata) {
		t.Fatal("shouldShowUpdateNotice for dev channel = true, want false")
	}
}

func TestShouldShowUpdateNoticeSkipsNonInteractiveTerminal(t *testing.T) {
	withoutUpdateNoticeEnv(t)
	withUpdateNoticeDisplay(t, false)
	root := &cobra.Command{Use: "turnal"}
	status := &cobra.Command{Use: "status"}
	root.AddCommand(status)

	if shouldShowUpdateNotice(status, updateNoticeTestMetadata()) {
		t.Fatal("shouldShowUpdateNotice = true, want false")
	}
}

func updateNoticeTestMetadata() upgrade.Metadata {
	return upgrade.Metadata{
		Version:       "0.4.1",
		Channel:       upgrade.ChannelStable,
		InstallSource: upgrade.InstallSourceNPM,
	}
}

func withUpdateNoticeDisplay(t *testing.T, enabled bool) {
	t.Helper()
	old := updateNoticeCanDisplay
	updateNoticeCanDisplay = func() bool {
		return enabled
	}
	t.Cleanup(func() {
		updateNoticeCanDisplay = old
	})
}

func withoutUpdateNoticeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("CI", "")
	t.Setenv("TURNAL_NO_UPDATE_CHECK", "")
}
