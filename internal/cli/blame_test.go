package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	blameengine "github.com/AadiJo/turnal/internal/blame"
	eventlog "github.com/AadiJo/turnal/internal/events"
	"github.com/AadiJo/turnal/internal/notes"
	"github.com/AadiJo/turnal/internal/primitives"
	"github.com/AadiJo/turnal/internal/provenance"
)

func TestBlameCommandShowsLineAttribution(t *testing.T) {
	root, repo, sessionID, turnID := createTurnWithDiff(t)
	t.Chdir(root.String())

	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypePromptUser,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"text":"change app.txt"}`),
	}); err != nil {
		t.Fatalf("append prompt event: %v", err)
	}
	if _, err := eventlog.Open(repo.MetadataDir).Append(eventlog.AppendInput{
		SessionID: sessionID,
		TurnID:    &turnID,
		Type:      primitives.EventTypeToolCall,
		Adapter:   primitives.AdapterCodex,
		Payload:   json.RawMessage(`{"tool_name":"apply_patch"}`),
	}); err != nil {
		t.Fatalf("append tool event: %v", err)
	}

	jsonOutput := runRootStdout(t, "blame", "app.txt:1", "--json")
	var result blameengine.Result
	if err := json.Unmarshal([]byte(jsonOutput), &result); err != nil {
		t.Fatalf("unmarshal blame json: %v\n%s", err, jsonOutput)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries len = %d, want 1", len(result.Entries))
	}
	entry := result.Entries[0]
	if entry.Text != "after" {
		t.Fatalf("entry text = %q, want after", entry.Text)
	}
	if entry.Origin.SessionID != sessionID || entry.Origin.TurnID != turnID {
		t.Fatalf("origin = %#v, want %s turn %s", entry.Origin, sessionID, turnID)
	}
	if entry.Origin.Prompt != "change app.txt" {
		t.Fatalf("prompt = %q, want change app.txt", entry.Origin.Prompt)
	}
	if len(entry.Origin.ToolNames) != 1 || entry.Origin.ToolNames[0] != "apply_patch" {
		t.Fatalf("tools = %#v, want apply_patch", entry.Origin.ToolNames)
	}

	textOutput := runRootStdout(t, "blame", "app.txt:1", "--verbose")
	for _, want := range []string{
		"[codex ",
		"] turn 1",
		"1 | after",
		"adapter: codex",
		"problem: no agent intent recorded for this change",
		"human request: change app.txt",
		"tools: apply_patch",
	} {
		if !strings.Contains(textOutput, want) {
			t.Fatalf("text output missing %q:\n%s", want, textOutput)
		}
	}
}

func TestBlameTextUsesLogStyleTurnLabelAndPrompt(t *testing.T) {
	sessionID := sessionID(t, "codex-sess_7f3a9c2d")
	turnID, _ := primitives.NewTurnID(2)
	sessionStartedAt := time.Date(2026, 7, 6, 3, 12, 0, 0, time.UTC)
	turnAt := time.Date(2026, 7, 6, 3, 15, 0, 0, time.UTC)

	var out bytes.Buffer
	err := writeBlameText(&out, blameengine.Result{
		Path: "hello_world.py",
		Sessions: []blameengine.SessionSummary{
			{
				ID:        sessionID,
				Adapter:   "codex",
				StartedAt: sessionStartedAt,
			},
		},
		Entries: []blameengine.Entry{
			{
				Line: 2,
				Text: "    for _ in range(5):",
				Origin: blameengine.Origin{
					Kind:      "turn",
					SessionID: sessionID,
					TurnID:    turnID,
					Time:      turnAt,
					Adapter:   "codex",
					Prompt:    "make a python script to say hello world",
				},
			},
		},
	}, false)
	if err != nil {
		t.Fatalf("writeBlameText: %v", err)
	}

	output := out.String()
	for _, want := range []string{
		"03:15 [codex 03:12] turn 2      2 |     for _ in range(5):",
		"Intent: no agent intent recorded for this change",
		"Human request: \"make a python script to say hello world\"",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("blame text missing %q:\n%s", want, output)
		}
	}
}

func TestBlameIntentConfidenceLabelsUnknownStatusHonestly(t *testing.T) {
	origin := blameengine.Origin{Intent: &provenance.Attribution{
		Status:     provenance.IntentStatus("future-status"),
		Confidence: provenance.IntentConfidenceLow,
	}}
	got := blameIntentConfidence(origin)
	if !strings.Contains(got, `unknown intent status "future-status"`) || strings.Contains(got, "stated before edit") {
		t.Fatalf("confidence = %q", got)
	}
}

func TestBlameTextShowsLowConfidenceIntentWithoutVerbose(t *testing.T) {
	seq, _ := primitives.NewEventSeq(4)
	var out bytes.Buffer
	err := writeBlameText(&out, blameengine.Result{Entries: []blameengine.Entry{{
		Line: 1,
		Text: "changed",
		Origin: blameengine.Origin{Kind: "turn", Intent: &provenance.Attribution{
			Problem: "retry state was stale", EventSeq: seq,
			Status: provenance.IntentStatusLateOutOfScope, Timing: provenance.IntentTimingAfter, Confidence: provenance.IntentConfidenceLow,
		}},
	}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if output := out.String(); !strings.Contains(output, "Intent: retry state was stale") || !strings.Contains(output, "Intent note: low (stated after edit; outside stated scope)") {
		t.Fatalf("blame output = %q", output)
	}
}

func TestBlameTextDistinguishesMissingAndAmbiguousIntent(t *testing.T) {
	var out bytes.Buffer
	err := writeBlameText(&out, blameengine.Result{Entries: []blameengine.Entry{
		{Line: 1, Text: "one", Origin: blameengine.Origin{Kind: "ambiguous"}},
		{Line: 2, Text: "two", Origin: blameengine.Origin{Kind: "concurrent"}},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{
		"Intent: unavailable because no recorded intent could be safely tied to this change",
		"Intent: unavailable because concurrent agent turns overlapped",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("blame output missing %q: %s", want, output)
		}
	}
}

// A note anchored to a range covers several lines. It must print once, not once
// per covered line, and it must not repeat for every entry sharing an origin.
func TestBlameTextPrintsEachNoteOnce(t *testing.T) {
	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}
	noteID, err := primitives.ParseNoteID("note_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("ParseNoteID: %v", err)
	}
	origin := blameengine.Origin{Kind: "turn", SessionID: "demo", TurnID: 1}
	result := blameengine.Result{
		Path: path,
		Entries: []blameengine.Entry{
			{Line: 1, Text: "alpha", Origin: origin},
			{Line: 2, Text: "beta", Origin: origin},
			{Line: 3, Text: "gamma", Origin: origin},
		},
		Notes: []blameengine.FileNote{{
			Note: notes.Note{
				NoteID: noteID,
				Text:   "this range is the regression",
				Anchor: &notes.Anchor{Path: path, LineStart: 1, LineEnd: 3},
			},
			Line: 1, LineEnd: 3,
		}},
	}

	var out bytes.Buffer
	if err := writeBlameText(&out, result, false); err != nil {
		t.Fatalf("writeBlameText: %v", err)
	}
	if count := strings.Count(out.String(), "this range is the regression"); count != 1 {
		t.Fatalf("note printed %d times, want exactly once:\n%s", count, out.String())
	}
	if !strings.Contains(out.String(), "Note on 1-3:") {
		t.Fatalf("range label missing:\n%s", out.String())
	}
}

// Note text is human-authored and, once shared history carries it, may arrive
// from another machine. An escape sequence must never reach the terminal intact.
func TestBlameTextEscapesNoteControlCharacters(t *testing.T) {
	path, err := primitives.ParseRepoPath("app.txt")
	if err != nil {
		t.Fatalf("ParseRepoPath: %v", err)
	}
	result := blameengine.Result{
		Path:    path,
		Entries: []blameengine.Entry{{Line: 1, Text: "alpha", Origin: blameengine.Origin{Kind: "turn"}}},
		Notes: []blameengine.FileNote{{
			Note: notes.Note{Text: "danger\x1b[31mred", Anchor: &notes.Anchor{Path: path, LineStart: 1}},
			Line: 1,
		}},
	}

	var out bytes.Buffer
	if err := writeBlameText(&out, result, false); err != nil {
		t.Fatalf("writeBlameText: %v", err)
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Fatalf("raw escape reached the terminal: %q", out.String())
	}
	if !strings.Contains(out.String(), "\\u001b") {
		t.Fatalf("escape was not rendered safely:\n%s", out.String())
	}
}

// An anchor path occupies one line beside note labels, so a newline in it must
// not be able to forge an additional line of Turnal output.
func TestNoteLineFieldsEscapeNewlineAndTab(t *testing.T) {
	forged := escapeNoteLine("src/a\nNote on 99: forged")
	if strings.ContainsAny(forged, "\n\t") {
		t.Fatalf("single-line escaping preserved layout characters: %q", forged)
	}
	if !strings.Contains(forged, "\\u000a") {
		t.Fatalf("newline was not escaped: %q", forged)
	}
	if tabbed := escapeNoteLine("a\tb"); !strings.Contains(tabbed, "\\u0009") {
		t.Fatalf("tab was not escaped: %q", tabbed)
	}
	// Note prose keeps newline and tab, which are legitimate formatting there.
	if body := escapeNoteText("first\nsecond"); !strings.Contains(body, "\n") {
		t.Fatalf("note body escaping dropped legitimate formatting: %q", body)
	}
}
