package index

import (
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/checkpoint"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

func TestBlameCacheRoundTripsActionIntent(t *testing.T) {
	requireGit(t)
	root := workspaceRoot(t)
	repo, err := checkpoint.Init(root)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := sessionID(t, "intent-cache")
	turnID, _ := primitives.NewTurnID(1)
	writeFile(t, root, "app.txt", "changed\n")
	latest, err := repo.CreateCheckpoint(sessionID, turnID, primitives.CheckpointPhasePost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Rebuild(repo); err != nil {
		t.Fatal(err)
	}
	store, err := Open(repo.MetadataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	path, _ := primitives.ParseRepoPath("app.txt")
	intentSeq, _ := primitives.NewEventSeq(4)
	intent := &provenance.Attribution{
		Problem:    "retry delay survives a successful request",
		Scope:      []string{"app.txt"},
		Evidence:   []string{"test:TestRetryReset"},
		EventSeq:   intentSeq,
		Status:     provenance.IntentStatusCaptured,
		Timing:     provenance.IntentTimingBefore,
		Confidence: provenance.IntentConfidenceHigh,
	}
	snapshot := BlameCacheSnapshot{
		Path:          path,
		HistoryKey:    "intent-history",
		LatestRef:     latest.Ref,
		LatestCommit:  latest.Commit,
		LatestTime:    time.Now().UTC(),
		CompleteTurns: 1,
		LineCount:     1,
		Entries: []BlameCacheEntry{{
			Line: 1,
			Text: "changed",
			Origin: BlameCacheOrigin{
				Kind:       "turn",
				SessionID:  sessionID,
				TurnID:     turnID,
				ActionTool: "apply_patch",
				Intent:     intent,
			},
		}},
	}
	if err := store.SaveBlameCache(snapshot); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.LoadBlameCache(BlameCacheQuery{
		Path:          path,
		HistoryKey:    snapshot.HistoryKey,
		LatestRef:     latest.Ref,
		LatestCommit:  latest.Commit,
		CompleteTurns: 1,
		Line:          1,
	})
	if err != nil || !ok || len(loaded.Entries) != 1 {
		t.Fatalf("loaded = %#v, ok=%v, err=%v", loaded, ok, err)
	}
	origin := loaded.Entries[0].Origin
	if origin.ActionTool != "apply_patch" || origin.Intent == nil || origin.Intent.Problem != intent.Problem || origin.Intent.Confidence != provenance.IntentConfidenceHigh {
		t.Fatalf("cached origin = %#v", origin)
	}
}
