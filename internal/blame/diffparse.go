package blame

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
)

type hunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []diffLine
}

type diffLine struct {
	Kind byte
	Text string
}

var hunkHeaderPattern = regexp.MustCompile(`^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

func parseUnifiedHunks(patch []byte) ([]hunk, error) {
	scanner := bufio.NewScanner(bytes.NewReader(patch))
	scanner.Buffer(make([]byte, 1024), 16*1024*1024)

	var hunks []hunk
	var current *hunk
	var oldSeen int
	var newSeen int
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 3 || line[:3] != "@@ " {
			if current != nil {
				done, err := collectHunkLine(current, line, &oldSeen, &newSeen)
				if err != nil {
					return nil, err
				}
				if done {
					hunks = append(hunks, *current)
					current = nil
					oldSeen = 0
					newSeen = 0
				}
			}
			continue
		}
		parsed, err := parseHunkHeader(line)
		if err != nil {
			return nil, err
		}
		if current != nil {
			return nil, fmt.Errorf("hunk starting at -%d +%d ended early: saw %d/%d old lines and %d/%d new lines", current.OldStart, current.NewStart, oldSeen, current.OldCount, newSeen, current.NewCount)
		}
		current = &parsed
		if parsed.OldCount == 0 && parsed.NewCount == 0 {
			hunks = append(hunks, parsed)
			current = nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan unified diff: %w", err)
	}
	if current != nil {
		if oldSeen != current.OldCount || newSeen != current.NewCount {
			return nil, fmt.Errorf("hunk starting at -%d +%d ended early: saw %d/%d old lines and %d/%d new lines", current.OldStart, current.NewStart, oldSeen, current.OldCount, newSeen, current.NewCount)
		}
		hunks = append(hunks, *current)
	}
	return hunks, nil
}

func collectHunkLine(current *hunk, line string, oldSeen *int, newSeen *int) (bool, error) {
	if line == `\ No newline at end of file` {
		return false, nil
	}
	if line == "" {
		return false, fmt.Errorf("empty line inside hunk starting at -%d +%d", current.OldStart, current.NewStart)
	}

	kind := line[0]
	switch kind {
	case ' ':
		(*oldSeen)++
		(*newSeen)++
	case '-':
		(*oldSeen)++
	case '+':
		(*newSeen)++
	default:
		return false, fmt.Errorf("unexpected diff line %q inside hunk starting at -%d +%d", line, current.OldStart, current.NewStart)
	}
	current.Lines = append(current.Lines, diffLine{Kind: kind, Text: line[1:]})

	if *oldSeen > current.OldCount || *newSeen > current.NewCount {
		return false, fmt.Errorf("hunk starting at -%d +%d exceeded declared size", current.OldStart, current.NewStart)
	}
	return *oldSeen == current.OldCount && *newSeen == current.NewCount, nil
}

func parseHunkHeader(line string) (hunk, error) {
	matches := hunkHeaderPattern.FindStringSubmatch(line)
	if matches == nil {
		return hunk{}, fmt.Errorf("parse hunk header %q", line)
	}

	oldStart, err := strconv.Atoi(matches[1])
	if err != nil {
		return hunk{}, fmt.Errorf("parse old hunk start in %q: %w", line, err)
	}
	oldCount, err := parseHunkCount(matches[2])
	if err != nil {
		return hunk{}, fmt.Errorf("parse old hunk count in %q: %w", line, err)
	}
	newStart, err := strconv.Atoi(matches[3])
	if err != nil {
		return hunk{}, fmt.Errorf("parse new hunk start in %q: %w", line, err)
	}
	newCount, err := parseHunkCount(matches[4])
	if err != nil {
		return hunk{}, fmt.Errorf("parse new hunk count in %q: %w", line, err)
	}

	return hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
	}, nil
}

func parseHunkCount(value string) (int, error) {
	if value == "" {
		return 1, nil
	}
	return strconv.Atoi(value)
}

func applyHunks(origins []Origin, hunks []hunk, origin Origin) ([]Origin, error) {
	applied := append([]Origin(nil), origins...)
	moved, err := movedOriginPool(origins, hunks)
	if err != nil {
		return nil, err
	}

	delta := 0
	for _, hunk := range hunks {
		index := 0
		if hunk.OldStart > 0 {
			index = hunk.OldStart - 1
		}
		index += delta
		if index < 0 || index > len(applied) {
			return nil, fmt.Errorf("hunk start %d maps outside %d tracked lines", hunk.OldStart, len(applied))
		}
		if index+hunk.OldCount > len(applied) {
			return nil, fmt.Errorf("hunk removes %d lines at %d from %d tracked lines", hunk.OldCount, index+1, len(applied))
		}

		next := make([]Origin, 0, len(applied)-hunk.OldCount+hunk.NewCount)
		next = append(next, applied[:index]...)
		cursor := index
		oldSeen := 0
		newSeen := 0
		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				if cursor >= len(applied) {
					return nil, fmt.Errorf("hunk context at %d maps outside %d tracked lines", cursor+1, len(applied))
				}
				next = append(next, applied[cursor])
				cursor++
				oldSeen++
				newSeen++
			case '-':
				cursor++
				oldSeen++
			case '+':
				if movedOrigin, ok := popMovedOrigin(moved, line.Text); ok {
					next = append(next, movedOrigin)
				} else {
					next = append(next, origin)
				}
				newSeen++
			default:
				return nil, fmt.Errorf("unsupported diff line kind %q", line.Kind)
			}
		}
		if oldSeen != hunk.OldCount || newSeen != hunk.NewCount {
			return nil, fmt.Errorf("hunk line counts old=%d/%d new=%d/%d", oldSeen, hunk.OldCount, newSeen, hunk.NewCount)
		}
		next = append(next, applied[cursor:]...)
		applied = next
		delta += hunk.NewCount - hunk.OldCount
	}
	return applied, nil
}

func movedOriginPool(origins []Origin, hunks []hunk) (map[string][]Origin, error) {
	moved := make(map[string][]Origin)
	for _, hunk := range hunks {
		index := 0
		if hunk.OldStart > 0 {
			index = hunk.OldStart - 1
		}
		oldSeen := 0
		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				index++
				oldSeen++
			case '-':
				if index < 0 || index >= len(origins) {
					return nil, fmt.Errorf("hunk removes line %d outside %d tracked lines", index+1, len(origins))
				}
				moved[line.Text] = append(moved[line.Text], origins[index])
				index++
				oldSeen++
			case '+':
			default:
				return nil, fmt.Errorf("unsupported diff line kind %q", line.Kind)
			}
		}
		if oldSeen != hunk.OldCount {
			return nil, fmt.Errorf("hunk line counts old=%d/%d", oldSeen, hunk.OldCount)
		}
	}
	return moved, nil
}

func popMovedOrigin(moved map[string][]Origin, text string) (Origin, bool) {
	origins := moved[text]
	if len(origins) == 0 {
		return Origin{}, false
	}
	origin := origins[0]
	if len(origins) == 1 {
		delete(moved, text)
	} else {
		moved[text] = origins[1:]
	}
	return origin, true
}
