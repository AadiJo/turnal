package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/spf13/cobra"
)

const maxDiffDocumentBytes = 4 << 20

type diffDocumentsOutput struct {
	Kind      string             `json:"kind"`
	SessionID string             `json:"session_id"`
	TurnID    uint64             `json:"turn_id"`
	Files     []diffDocumentJSON `json:"files"`
}

type diffDocumentJSON struct {
	Status       string `json:"status"`
	Path         string `json:"path"`
	OldPath      string `json:"old_path,omitempty"`
	Additions    int    `json:"additions,omitempty"`
	Deletions    int    `json:"deletions,omitempty"`
	Binary       bool   `json:"binary,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	BeforeExists bool   `json:"before_exists"`
	AfterExists  bool   `json:"after_exists"`
	BeforeBase64 string `json:"before_base64,omitempty"`
	AfterBase64  string `json:"after_base64,omitempty"`
}

func diffCmd() *cobra.Command {
	var session string
	var turn uint64
	var preRef string
	var postRef string
	var jsonOutput bool
	var rollbackPreview bool

	cmd := &cobra.Command{
		Use:          "diff [session:turn]",
		Short:        "Show the diff between hidden Git checkpoints",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, err := openCheckpointRepo()
			if err != nil {
				return err
			}
			if rollbackPreview && !jsonOutput {
				return fmt.Errorf("--rollback-preview requires --json")
			}
			if jsonOutput {
				if len(args) != 1 {
					return fmt.Errorf("--json requires a session:turn target")
				}
				if session != "" || turn != 0 || preRef != "" || postRef != "" {
					return fmt.Errorf("target argument cannot be combined with --session, --turn, --pre-ref, or --post-ref")
				}
				sessionID, turnID, err := parseTurnTarget(args[0])
				if err != nil {
					return err
				}
				if rollbackPreview {
					return writeRollbackDiffJSON(cmd.OutOrStdout(), repo, sessionID, turnID)
				}
				return writeTurnDiffJSON(cmd.OutOrStdout(), repo, sessionID, turnID)
			}

			var diff []byte
			switch {
			case len(args) == 1:
				if session != "" || turn != 0 || preRef != "" || postRef != "" {
					return fmt.Errorf("target argument cannot be combined with --session, --turn, --pre-ref, or --post-ref")
				}
				sessionID, turnID, err := parseTurnTarget(args[0])
				if err != nil {
					return err
				}
				diff, err = repo.DiffTurn(sessionID, turnID)
				if err != nil {
					return err
				}
			case preRef != "" || postRef != "":
				if preRef == "" || postRef == "" {
					return fmt.Errorf("--pre-ref and --post-ref must be provided together")
				}
				pre, err := primitives.ParseCheckpointRef(preRef)
				if err != nil {
					return err
				}
				post, err := primitives.ParseCheckpointRef(postRef)
				if err != nil {
					return err
				}
				diff, err = repo.DiffRefs(pre, post)
				if err != nil {
					return err
				}
			default:
				if session == "" {
					return fmt.Errorf("--session is required")
				}
				if turn == 0 {
					return fmt.Errorf("--turn must be greater than zero")
				}
				sessionID, err := primitives.ParseSessionID(session)
				if err != nil {
					return err
				}
				turnID, err := primitives.NewTurnID(turn)
				if err != nil {
					return err
				}
				diff, err = repo.DiffTurn(sessionID, turnID)
				if err != nil {
					return err
				}
			}

			_, err = cmd.OutOrStdout().Write(diff)
			return err
		},
	}

	cmd.Flags().StringVar(&session, "session", "", "Session id to diff")
	cmd.Flags().Uint64Var(&turn, "turn", 0, "Turn number to diff")
	cmd.Flags().StringVar(&preRef, "pre-ref", "", "Explicit pre checkpoint ref")
	cmd.Flags().StringVar(&postRef, "post-ref", "", "Explicit post checkpoint ref")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Emit changed files and checkpoint contents as JSON")
	cmd.Flags().BoolVar(&rollbackPreview, "rollback-preview", false, "Compare the workspace with the pre-turn rollback target")
	for _, name := range []string{"session", "turn", "pre-ref", "post-ref"} {
		_ = cmd.Flags().MarkHidden(name)
	}
	_ = cmd.Flags().MarkHidden("rollback-preview")
	return cmd
}

func writeTurnDiffJSON(
	w io.Writer,
	repo *checkpoint.Repo,
	sessionID primitives.SessionID,
	turnID primitives.TurnID,
) error {
	preRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return err
	}
	postRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		return err
	}
	preCommit, err := repo.CheckpointCommit(preRef)
	if err != nil {
		return err
	}
	postCommit, err := repo.CheckpointCommit(postRef)
	if err != nil {
		return err
	}
	changes, err := repo.DiffNameStatusRefs(preRef, postRef)
	if err != nil {
		return err
	}
	summary, err := repo.DiffStatRefs(preRef, postRef)
	if err != nil {
		return err
	}
	stats := make(map[string]checkpoint.DiffFileStat, len(summary.Files))
	for _, stat := range summary.Files {
		stats[stat.Path] = stat
	}

	output := diffDocumentsOutput{
		Kind:      "turn",
		SessionID: sessionID.String(),
		TurnID:    turnID.Uint64(),
		Files:     make([]diffDocumentJSON, 0, len(changes)),
	}
	for _, change := range changes {
		beforePath := change.Path
		if change.OldPath != "" {
			beforePath = change.OldPath
		}
		before, beforeExists, err := repo.CommitFileBytesIfExists(preCommit, beforePath)
		if err != nil {
			return err
		}
		after, afterExists, err := repo.CommitFileBytesIfExists(postCommit, change.Path)
		if err != nil {
			return err
		}
		stat := stats[change.Path]
		if change.OldPath != "" {
			oldStat := stats[change.OldPath]
			stat.Additions += oldStat.Additions
			stat.Deletions += oldStat.Deletions
			stat.Binary = stat.Binary || oldStat.Binary
		}
		document := diffDocumentJSON{
			Status:       change.Status,
			Path:         change.Path,
			OldPath:      change.OldPath,
			Additions:    stat.Additions,
			Deletions:    stat.Deletions,
			Binary:       stat.Binary,
			BeforeExists: beforeExists,
			AfterExists:  afterExists,
		}
		document.BeforeBase64, document.Binary, document.Truncated = encodeDiffDocument(before, beforeExists, document.Binary)
		var afterBinary, afterTruncated bool
		document.AfterBase64, afterBinary, afterTruncated = encodeDiffDocument(after, afterExists, document.Binary)
		document.Binary = document.Binary || afterBinary
		document.Truncated = document.Truncated || afterTruncated
		if document.Binary || document.Truncated {
			document.BeforeBase64 = ""
			document.AfterBase64 = ""
		}
		output.Files = append(output.Files, document)
	}
	return writeDiffDocumentsJSON(w, output)
}

func writeRollbackDiffJSON(
	w io.Writer,
	repo *checkpoint.Repo,
	sessionID primitives.SessionID,
	turnID primitives.TurnID,
) error {
	preRef, err := repo.CheckpointRefFor(sessionID, turnID, primitives.CheckpointPhasePre)
	if err != nil {
		return err
	}
	targetCommit, err := repo.CheckpointCommit(preRef)
	if err != nil {
		return err
	}
	plan, err := repo.PlanRestoreCommit(targetCommit)
	if err != nil {
		return err
	}
	output := diffDocumentsOutput{
		Kind:      "rollback",
		SessionID: sessionID.String(),
		TurnID:    turnID.Uint64(),
		Files:     make([]diffDocumentJSON, 0, len(plan.Changes)),
	}
	for _, change := range plan.Changes {
		after, afterExists, err := repo.CommitFileBytesIfExists(targetCommit, change.Path)
		if err != nil {
			return err
		}
		document := diffDocumentJSON{
			Status:       restoreActionStatus(change.Action),
			Path:         change.Path,
			BeforeExists: change.Action != checkpoint.RestoreActionAdded,
			AfterExists:  afterExists,
		}
		document.AfterBase64, document.Binary, document.Truncated = encodeDiffDocument(after, afterExists, false)
		if document.Binary || document.Truncated {
			document.AfterBase64 = ""
		}
		output.Files = append(output.Files, document)
	}
	return writeDiffDocumentsJSON(w, output)
}

func encodeDiffDocument(content []byte, exists bool, binary bool) (encoded string, detectedBinary bool, truncated bool) {
	if !exists {
		return "", binary, false
	}
	if len(content) > maxDiffDocumentBytes {
		return "", binary, true
	}
	binary = binary || bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content)
	if binary {
		return "", true, false
	}
	return base64.StdEncoding.EncodeToString(content), false, false
}

func restoreActionStatus(action checkpoint.RestoreAction) string {
	switch action {
	case checkpoint.RestoreActionAdded:
		return "A"
	case checkpoint.RestoreActionDeleted:
		return "D"
	case checkpoint.RestoreActionModeChanged:
		return "T"
	default:
		return "M"
	}
}

func writeDiffDocumentsJSON(w io.Writer, output diffDocumentsOutput) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(output)
}
