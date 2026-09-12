//go:build !windows

package adapters

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestTranscriptReadersRejectFIFOWithoutWaiting(t *testing.T) {
	for _, reader := range []struct {
		name string
		read func(hookPayload) bool
	}{
		{"claude usage", func(p hookPayload) bool { return claudeCumulativeUsage(p) == nil }},
		{"codex usage", func(p hookPayload) bool { return codexCumulativeUsage(p) == nil }},
		{"claude model", func(p hookPayload) bool { return claudeCompletedTurnModel(p) == "" }},
	} {
		t.Run(reader.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-1.jsonl")
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan bool, 1)
			go func() {
				done <- reader.read(hookPayload{SessionID: "session-1", TranscriptPath: path, LastAssistantMessage: "done"})
			}()
			select {
			case rejected := <-done:
				if !rejected {
					t.Fatal("accepted a FIFO transcript")
				}
			case <-time.After(time.Second):
				// Release a blocked reader so a failing test leaves no goroutine behind.
				writer, err := os.OpenFile(path, os.O_RDWR|syscall.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				<-done
				_ = writer.Close()
				t.Fatal("transcript reader blocked opening a FIFO with no writer")
			}
		})
	}
}
