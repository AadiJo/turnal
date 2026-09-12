package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/AadiJo/turnal/internal/primitives"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkEventLogPaths(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		for _, mode := range []string{"missing-source", "append-source", "append-no-source"} {
			b.Run(fmt.Sprintf("%s/%d", mode, n), func(b *testing.B) {
				log := Open(b.TempDir())
				log.RepoID, _ = primitives.NewRepoID()
				log.WorktreeID, _ = primitives.NewWorktreeID()
				log.ProducerID, _ = primitives.NewEventProducerID()
				log.Aggregate = false
				sid, _ := primitives.ParseSessionID("audit")
				stream, _ := primitives.DeriveEventStreamID(log.ProducerID, sid)
				var buf bytes.Buffer
				previous := GenesisHash
				var last Event
				for i := 1; i <= n; i++ {
					seq, _ := primitives.NewEventSeq(uint64(i))
					e := Event{Version: 2, RepoID: log.RepoID, WorktreeID: log.WorktreeID, StreamID: stream, SessionID: sid, Seq: seq, Type: primitives.EventTypeToolCall, Time: primitives.NowTimestamp(), SourceID: fmt.Sprintf("seed-%d", i), PrevHash: previous, Payload: json.RawMessage(`{"tool_name":"read_file","text":"small representative event"}`)}
					var err error
					e.Hash, err = eventHash(e)
					if err != nil {
						b.Fatal(err)
					}
					data, err := json.Marshal(e)
					if err != nil {
						b.Fatal(err)
					}
					buf.Write(data)
					buf.WriteByte('\n')
					previous = e.Hash
					last = e
				}
				path := log.streamPath(sid, stream)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
					b.Fatal(err)
				}
				if err := log.writeTailState(path, last); err != nil {
					b.Fatal(err)
				}
				if mode != "append-no-source" {
					if _, err := log.ContainsSourceID(sid, "warmup"); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if mode == "missing-source" {
						_, err := log.ContainsSourceID(sid, "missing")
						if err != nil {
							b.Fatal(err)
						}
					} else {
						source := ""
						if mode == "append-source" {
							source = fmt.Sprintf("new-%d", i)
						}
						_, err := log.Append(AppendInput{SessionID: sid, Type: primitives.EventTypeToolCall, SourceID: source, Payload: json.RawMessage(`{"tool_name":"read_file"}`)})
						if err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
