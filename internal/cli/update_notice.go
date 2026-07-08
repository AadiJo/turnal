package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AadiJo/turnal/internal/upgrade"
	"github.com/spf13/cobra"
)

const updateNoticeTimeout = 2 * time.Second

var updateNoticeCanDisplay = func() bool {
	return fileIsTerminal(os.Stderr)
}

func maybeShowUpdateNotice(rootCmd *cobra.Command, executedCmd *cobra.Command) {
	if !shouldShowUpdateNotice(executedCmd, currentBuildMetadata()) {
		return
	}
	cachePath, err := upgrade.DefaultNoticeCachePath()
	if err != nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateNoticeTimeout)
	defer cancel()
	notice, ok, err := upgrade.CheckUpdateNotice(ctx, upgrade.NoticeOptions{
		Current:  currentBuildMetadata(),
		Registry: newUpgradeRegistry(),
		Cache:    upgrade.FileNoticeCache{Path: cachePath},
	})
	if err != nil || !ok {
		return
	}
	fmt.Fprintln(rootCmd.ErrOrStderr())
	fmt.Fprintln(rootCmd.ErrOrStderr(), notice.Message())
}

func shouldShowUpdateNotice(cmd *cobra.Command, metadata upgrade.Metadata) bool {
	if cmd == nil {
		return false
	}
	metadata = metadata.Normalize()
	if metadata.InstallSource != upgrade.InstallSourceNPM {
		return false
	}
	if metadata.Channel != upgrade.ChannelStable && metadata.Channel != upgrade.ChannelNightly {
		return false
	}
	if envTruthy("CI") || envTruthy("TURNAL_NO_UPDATE_CHECK") {
		return false
	}
	if !updateNoticeCanDisplay() {
		return false
	}
	if commandOrAncestorHidden(cmd) {
		return false
	}
	if commandUsesJSONOutput(cmd) {
		return false
	}

	switch topLevelCommandName(cmd) {
	case "", "help", "upgrade", "version":
		return false
	default:
		return true
	}
}

func commandUsesJSONOutput(cmd *cobra.Command) bool {
	if flag := cmd.Flag("json"); flag != nil && flag.Changed && flag.Value.String() == "true" {
		return true
	}
	return false
}

func commandOrAncestorHidden(cmd *cobra.Command) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if current.Hidden {
			return true
		}
	}
	return false
}

func topLevelCommandName(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	current := cmd
	for current.Parent() != nil && current.Parent().Parent() != nil {
		current = current.Parent()
	}
	if current.Parent() == nil {
		return ""
	}
	return current.Name()
}

func envTruthy(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value != "" && value != "0" && value != "false"
}

func fileIsTerminal(file *os.File) bool {
	if file == nil {
		return false
	}
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}
