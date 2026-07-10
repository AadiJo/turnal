package telemetry

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTelemetrySchemaV1GoldenCoversEntireMetricRegistry(t *testing.T) {
	data, err := os.ReadFile("testdata/telemetry_schema_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	aggregate, err := DecodeDailyAggregate(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregate.Metrics) != len(metricNames) {
		t.Fatalf("golden metrics = %d, registry = %d", len(aggregate.Metrics), len(metricNames))
	}
	for _, metric := range aggregate.Metrics {
		if _, ok := metricNames[metric.Key]; !ok {
			t.Fatalf("golden contains unknown metric %d", metric.Key)
		}
	}
	encoded, err := EncodeDailyAggregate(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, data) {
		t.Fatalf("golden payload is not canonical\nwant: %s\ngot:  %s", data, encoded)
	}
}

func TestMetricRegistryRejectsUnknownValues(t *testing.T) {
	for key, name := range metricNames {
		parsed, err := ParseMetricKey(name)
		if err != nil {
			t.Fatalf("ParseMetricKey(%q): %v", name, err)
		}
		if parsed != key || parsed.String() != name {
			t.Fatalf("metric round trip = %d %q, want %d %q", parsed, parsed, key, name)
		}
	}
	if _, err := ParseMetricKey("command.secret.success"); err == nil {
		t.Fatal("unknown metric was accepted")
	}
}

func TestUUIDIsRandomVersionFourAndRoundTrips(t *testing.T) {
	first, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two generated UUIDs are equal")
	}
	parsed, err := ParseUUID(first.String())
	if err != nil || parsed != first {
		t.Fatalf("UUID round trip = %#v, %v", parsed, err)
	}
	for _, invalid := range []string{
		"",
		"167e8e5d-84fc-16bd-a39c-b67d47658f8e",
		"167e8e5d-84fc-46bd-739c-b67d47658f8e",
		"167e8e5d-84fc-46bd-a39c-b67d47658f8x",
	} {
		if _, err := ParseUUID(invalid); err == nil {
			t.Fatalf("ParseUUID(%q) succeeded", invalid)
		}
	}
}

func TestDailyAggregateCanonicalEncoding(t *testing.T) {
	id := mustUUID(t, "167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	aggregate, err := NewDailyAggregate(id, time.Date(2026, 7, 9, 23, 45, 0, 0, time.FixedZone("test", -5*60*60)), Build{
		Version:       "0.4.2",
		Channel:       ChannelStable,
		InstallSource: InstallSourceNPM,
		OS:            "linux",
		Arch:          "amd64",
	}, map[MetricKey]uint64{
		MetricTurnRecordedCodex:    7,
		MetricCommandStatusSuccess: 3,
		MetricCommandDiffSuccess:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeDailyAggregate(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	if aggregate.Date != "2026-07-10" {
		t.Fatalf("UTC date = %s, want 2026-07-10", aggregate.Date)
	}
	wantOrder := []string{"command.diff.success", "command.status.success", "turn.recorded.codex"}
	position := -1
	for _, name := range wantOrder {
		next := bytes.Index(data[position+1:], []byte(name))
		if next < 0 {
			t.Fatalf("encoded aggregate missing %q: %s", name, data)
		}
		position += next + 1
	}
	decoded, err := DecodeDailyAggregate(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.BatchID != aggregate.BatchID || decoded.AnonymousID != id || len(decoded.Metrics) != 3 {
		t.Fatalf("decoded aggregate = %#v", decoded)
	}
}

func TestDailyAggregateRejectsUnknownAndNonCanonicalData(t *testing.T) {
	id := "167e8e5d-84fc-46bd-a39c-b67d47658f8e"
	batch := "6f4914ce-d85d-49cb-80f5-e7dc3b118a07"
	valid := `{"schema_version":1,"batch_id":"` + batch + `","anonymous_id":"` + id + `","date":"2026-07-09","build":{"version":"0.4.2","channel":"stable","install_source":"npm","os":"linux","arch":"amd64"},"metrics":[{"key":"command.status.success","count":3}]}`
	for name, data := range map[string]string{
		"unknown root field":   strings.Replace(valid, `"schema_version":1`, `"schema_version":1,"workspace":"secret"`, 1),
		"unknown build field":  strings.Replace(valid, `"arch":"amd64"`, `"arch":"amd64","hostname":"secret"`, 1),
		"unknown metric field": strings.Replace(valid, `"count":3`, `"count":3,"path":"/secret"`, 1),
		"unknown metric":       strings.Replace(valid, "command.status.success", "command.secret.success", 1),
		"zero count":           strings.Replace(valid, `"count":3`, `"count":0`, 1),
		"trailing value":       valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeDailyAggregate([]byte(data)); err == nil {
				t.Fatalf("invalid aggregate accepted: %s", data)
			}
		})
	}
}

func TestPayloadContainsOnlyApprovedFields(t *testing.T) {
	forbidden := []string{
		"/home/alice/secret-repo",
		"github.com/acme/private",
		"feature/payroll",
		"sk-live-",
		"prompt text",
		"tool output",
		"gpt-",
		"alice@example.com",
	}
	id := mustUUID(t, "167e8e5d-84fc-46bd-a39c-b67d47658f8e")
	aggregate, err := NewDailyAggregate(id, time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC), Build{
		Version:       "0.4.2",
		Channel:       ChannelStable,
		InstallSource: InstallSourceNPM,
		OS:            "linux",
		Arch:          "amd64",
	}, map[MetricKey]uint64{MetricTurnRecordedCodex: 1})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeDailyAggregate(aggregate)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("telemetry leaked forbidden value %q", value)
		}
	}
}

func mustUUID(t *testing.T, value string) UUID {
	t.Helper()
	id, err := ParseUUID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
