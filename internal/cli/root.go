package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var version = "0.0.0"
var channel = "dev"
var commit = ""
var installSource = "unknown"

func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "turnal",
		Short:   "Local-first version control for AI agent activity",
		Version: version,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(worktreeCmd())
	rootCmd.AddCommand(mergeCmd())
	rootCmd.AddCommand(storeCmd())
	rootCmd.AddCommand(destroyCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(logCmd())
	rootCmd.AddCommand(sessionsCmd())
	rootCmd.AddCommand(reindexCmd())
	rootCmd.AddCommand(sessionCmd())
	rootCmd.AddCommand(retentionCmd())
	rootCmd.AddCommand(maintenanceCmd())
	rootCmd.AddCommand(showCmd())
	rootCmd.AddCommand(searchCmd())
	rootCmd.AddCommand(turnCmd())
	rootCmd.AddCommand(checkpointCmd())
	rootCmd.AddCommand(diffCmd())
	rootCmd.AddCommand(blameCmd())
	rootCmd.AddCommand(rollbackCmd())
	rootCmd.AddCommand(recoveryCmd())
	rootCmd.AddCommand(replayCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(upgradeCmd())
	rootCmd.AddCommand(analyticsCmd())
	rootCmd.AddCommand(claudeHookCmd())
	rootCmd.AddCommand(codexHookCmd())
	rootCmd.AddCommand(internalTelemetryFlushCmd())
	silenceSubcommandErrors(rootCmd)

	return rootCmd
}

func silenceSubcommandErrors(cmd *cobra.Command) {
	for _, subcommand := range cmd.Commands() {
		subcommand.SilenceErrors = true
		silenceSubcommandErrors(subcommand)
	}
}

func Execute() int {
	rootCmd := NewRootCmd()
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	return executeRoot(rootCmd)
}

func executeRoot(rootCmd *cobra.Command) int {
	executedCmd, err := rootCmd.ExecuteC()
	exitCode := 0
	if err != nil {
		if code, ok := commandExitCode(err); ok {
			exitCode = code
		} else {
			exitCode = 1
		}
		if _, isCommandExit := commandExitCode(err); !isCommandExit && !isUnknownCommandError(err) {
			fmt.Fprintln(rootCmd.ErrOrStderr(), err)
		}
	}
	recordCommandTelemetry(executedCmd, err)
	maybeScheduleTelemetryFlush(executedCmd)
	if err == nil && !maybeShowTelemetryNotice(rootCmd, executedCmd) {
		maybeShowUpdateNotice(rootCmd, executedCmd)
	}
	return exitCode
}

func commandExitCode(err error) (int, bool) {
	var commandErr commandExitError
	if errors.As(err, &commandErr) {
		return commandErr.ExitCode(), true
	}
	var childErr childExitError
	if errors.As(err, &childErr) {
		return childErr.ExitCode(), true
	}
	return 0, false
}

type commandExitError struct {
	code int
}

func (err commandExitError) Error() string {
	return fmt.Sprintf("command exited with status %d", err.code)
}

func (err commandExitError) ExitCode() int {
	if err.code < 0 {
		return 1
	}
	return err.code
}

func isUnknownCommandError(err error) bool {
	return strings.HasPrefix(err.Error(), "unknown command ")
}
