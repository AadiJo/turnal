package telemetry

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	SchemaVersion  = 1
	MaxMetricCount = 1_000_000
)

type MetricKey uint16

const (
	metricInvalid MetricKey = iota
	MetricInstallationActive
	MetricWorkspaceInitialized
	MetricTurnRecordedClaude
	MetricTurnRecordedCodex
	MetricCommandStatusSuccess
	MetricCommandStatusFailure
	MetricCommandLogSuccess
	MetricCommandLogFailure
	MetricCommandSessionsSuccess
	MetricCommandSessionsFailure
	MetricCommandShowSuccess
	MetricCommandShowFailure
	MetricCommandSearchSuccess
	MetricCommandSearchFailure
	MetricCommandDiffSuccess
	MetricCommandDiffFailure
	MetricCommandBlameSuccess
	MetricCommandBlameFailure
	MetricCommandRollbackSuccess
	MetricCommandRollbackFailure
	MetricCommandReplayCheckoutSuccess
	MetricCommandReplayCheckoutFailure
	MetricCommandReplayMoveSuccess
	MetricCommandReplayMoveFailure
	MetricCommandReplayRemoveSuccess
	MetricCommandReplayRemoveFailure
	MetricCommandRunSuccess
	MetricCommandRunChildFailure
	MetricAdapterConfiguredClaude
	MetricAdapterConfiguredCodex
	MetricFailureValidation
	MetricFailureIntegrity
	MetricFailureIO
)

var metricNames = map[MetricKey]string{
	MetricInstallationActive:           "installation.active",
	MetricWorkspaceInitialized:         "workspace.initialized",
	MetricTurnRecordedClaude:           "turn.recorded.claude",
	MetricTurnRecordedCodex:            "turn.recorded.codex",
	MetricCommandStatusSuccess:         "command.status.success",
	MetricCommandStatusFailure:         "command.status.failure",
	MetricCommandLogSuccess:            "command.log.success",
	MetricCommandLogFailure:            "command.log.failure",
	MetricCommandSessionsSuccess:       "command.sessions.success",
	MetricCommandSessionsFailure:       "command.sessions.failure",
	MetricCommandShowSuccess:           "command.show.success",
	MetricCommandShowFailure:           "command.show.failure",
	MetricCommandSearchSuccess:         "command.search.success",
	MetricCommandSearchFailure:         "command.search.failure",
	MetricCommandDiffSuccess:           "command.diff.success",
	MetricCommandDiffFailure:           "command.diff.failure",
	MetricCommandBlameSuccess:          "command.blame.success",
	MetricCommandBlameFailure:          "command.blame.failure",
	MetricCommandRollbackSuccess:       "command.rollback.success",
	MetricCommandRollbackFailure:       "command.rollback.failure",
	MetricCommandReplayCheckoutSuccess: "command.replay.checkout.success",
	MetricCommandReplayCheckoutFailure: "command.replay.checkout.failure",
	MetricCommandReplayMoveSuccess:     "command.replay.move.success",
	MetricCommandReplayMoveFailure:     "command.replay.move.failure",
	MetricCommandReplayRemoveSuccess:   "command.replay.remove.success",
	MetricCommandReplayRemoveFailure:   "command.replay.remove.failure",
	MetricCommandRunSuccess:            "command.run.success",
	MetricCommandRunChildFailure:       "command.run.child_failure",
	MetricAdapterConfiguredClaude:      "adapter.configured.claude",
	MetricAdapterConfiguredCodex:       "adapter.configured.codex",
	MetricFailureValidation:            "failure.validation",
	MetricFailureIntegrity:             "failure.integrity",
	MetricFailureIO:                    "failure.io",
}

var metricsByName = func() map[string]MetricKey {
	result := make(map[string]MetricKey, len(metricNames))
	for key, name := range metricNames {
		result[name] = key
	}
	return result
}()

func (key MetricKey) String() string {
	return metricNames[key]
}

func ParseMetricKey(value string) (MetricKey, error) {
	key, ok := metricsByName[value]
	if !ok {
		return metricInvalid, fmt.Errorf("unknown telemetry metric %q", value)
	}
	return key, nil
}

func (key MetricKey) Valid() bool {
	_, ok := metricNames[key]
	return ok
}

func (key MetricKey) MarshalJSON() ([]byte, error) {
	if !key.Valid() {
		return nil, fmt.Errorf("invalid telemetry metric %d", key)
	}
	return json.Marshal(key.String())
}

func (key *MetricKey) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := ParseMetricKey(value)
	if err != nil {
		return err
	}
	*key = parsed
	return nil
}

type UUID struct {
	value string
}

func NewUUID() (UUID, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return UUID{}, fmt.Errorf("generate telemetry UUID: %w", err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return UUID{value: formatUUID(raw)}, nil
}

func ParseUUID(value string) (UUID, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return UUID{}, fmt.Errorf("invalid UUID %q", value)
	}
	compact := strings.ReplaceAll(value, "-", "")
	raw, err := hex.DecodeString(compact)
	if err != nil || len(raw) != 16 {
		return UUID{}, fmt.Errorf("invalid UUID %q", value)
	}
	if raw[6]>>4 != 4 || raw[8]>>6 != 2 {
		return UUID{}, fmt.Errorf("telemetry UUID must be random version 4")
	}
	return UUID{value: value}, nil
}

func formatUUID(raw [16]byte) string {
	var encoded [32]byte
	hex.Encode(encoded[:], raw[:])
	return string(encoded[0:8]) + "-" + string(encoded[8:12]) + "-" +
		string(encoded[12:16]) + "-" + string(encoded[16:20]) + "-" + string(encoded[20:32])
}

func (id UUID) String() string {
	return id.value
}

func (id UUID) Valid() bool {
	_, err := ParseUUID(id.value)
	return err == nil
}

func (id UUID) MarshalJSON() ([]byte, error) {
	if !id.Valid() {
		return nil, errors.New("invalid telemetry UUID")
	}
	return json.Marshal(id.value)
}

func (id *UUID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	parsed, err := ParseUUID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

type ReleaseChannel string

const (
	ChannelStable  ReleaseChannel = "stable"
	ChannelNightly ReleaseChannel = "nightly"
)

func (channel ReleaseChannel) Valid() bool {
	return channel == ChannelStable || channel == ChannelNightly
}

type InstallSource string

const (
	InstallSourceNPM     InstallSource = "npm"
	InstallSourceSource  InstallSource = "source"
	InstallSourceUnknown InstallSource = "unknown"
)

func (source InstallSource) Valid() bool {
	switch source {
	case InstallSourceNPM, InstallSourceSource, InstallSourceUnknown:
		return true
	default:
		return false
	}
}

type Build struct {
	Version       string         `json:"version"`
	Channel       ReleaseChannel `json:"channel"`
	InstallSource InstallSource  `json:"install_source"`
	OS            string         `json:"os"`
	Arch          string         `json:"arch"`
}

func RuntimeBuild(version string, channel ReleaseChannel, source InstallSource) Build {
	return Build{
		Version:       version,
		Channel:       channel,
		InstallSource: source,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}

func (build Build) Validate() error {
	if !validSemver(build.Version) {
		return fmt.Errorf("invalid Turnal version %q", build.Version)
	}
	if !build.Channel.Valid() {
		return fmt.Errorf("invalid release channel %q", build.Channel)
	}
	if !build.InstallSource.Valid() {
		return fmt.Errorf("invalid install source %q", build.InstallSource)
	}
	switch build.OS {
	case "darwin", "linux", "windows":
	default:
		return fmt.Errorf("unsupported operating system %q", build.OS)
	}
	switch build.Arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("unsupported architecture %q", build.Arch)
	}
	return nil
}

func validSemver(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "v") {
		return false
	}
	coreAndPre := strings.SplitN(value, "+", 2)
	if len(coreAndPre) == 2 && !validSemverIdentifiers(coreAndPre[1], false) {
		return false
	}
	coreAndPre = strings.SplitN(coreAndPre[0], "-", 2)
	if len(coreAndPre) == 2 && !validSemverIdentifiers(coreAndPre[1], false) {
		return false
	}
	parts := strings.Split(coreAndPre[0], ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !validNumericIdentifier(part) {
			return false
		}
	}
	return true
}

func validNumericIdentifier(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validSemverIdentifiers(value string, rejectLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" || (rejectLeadingZero && len(identifier) > 1 && identifier[0] == '0') {
			return false
		}
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') && char != '-' {
				return false
			}
		}
	}
	return true
}

type MetricCount struct {
	Key   MetricKey `json:"key"`
	Count uint64    `json:"count"`
}

func (metric MetricCount) Validate() error {
	if !metric.Key.Valid() {
		return fmt.Errorf("invalid telemetry metric %d", metric.Key)
	}
	if metric.Count == 0 || metric.Count > MaxMetricCount {
		return fmt.Errorf("metric %s count must be between 1 and %d", metric.Key, MaxMetricCount)
	}
	return nil
}

type DailyAggregate struct {
	SchemaVersion int           `json:"schema_version"`
	BatchID       UUID          `json:"batch_id"`
	AnonymousID   UUID          `json:"anonymous_id"`
	Date          string        `json:"date"`
	Build         Build         `json:"build"`
	Metrics       []MetricCount `json:"metrics"`
}

func NewDailyAggregate(id UUID, date time.Time, build Build, counts map[MetricKey]uint64) (DailyAggregate, error) {
	batchID, err := NewUUID()
	if err != nil {
		return DailyAggregate{}, err
	}
	metrics := make([]MetricCount, 0, len(counts))
	for key, count := range counts {
		if count == 0 {
			continue
		}
		metrics = append(metrics, MetricCount{Key: key, Count: count})
	}
	sort.Slice(metrics, func(i, j int) bool {
		return metrics[i].Key.String() < metrics[j].Key.String()
	})
	aggregate := DailyAggregate{
		SchemaVersion: SchemaVersion,
		BatchID:       batchID,
		AnonymousID:   id,
		Date:          date.UTC().Format(time.DateOnly),
		Build:         build,
		Metrics:       metrics,
	}
	if err := aggregate.Validate(); err != nil {
		return DailyAggregate{}, err
	}
	return aggregate, nil
}

func (aggregate DailyAggregate) Validate() error {
	if aggregate.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported telemetry schema version %d", aggregate.SchemaVersion)
	}
	if !aggregate.BatchID.Valid() {
		return errors.New("invalid batch ID")
	}
	if !aggregate.AnonymousID.Valid() {
		return errors.New("invalid anonymous installation ID")
	}
	parsedDate, err := time.Parse(time.DateOnly, aggregate.Date)
	if err != nil || parsedDate.Format(time.DateOnly) != aggregate.Date {
		return fmt.Errorf("invalid UTC aggregate date %q", aggregate.Date)
	}
	if err := aggregate.Build.Validate(); err != nil {
		return err
	}
	if len(aggregate.Metrics) == 0 || len(aggregate.Metrics) > 64 {
		return fmt.Errorf("aggregate must contain between 1 and 64 metrics")
	}
	seen := make(map[MetricKey]struct{}, len(aggregate.Metrics))
	previous := ""
	for _, metric := range aggregate.Metrics {
		if err := metric.Validate(); err != nil {
			return err
		}
		if _, ok := seen[metric.Key]; ok {
			return fmt.Errorf("duplicate telemetry metric %s", metric.Key)
		}
		seen[metric.Key] = struct{}{}
		if previous != "" && metric.Key.String() <= previous {
			return errors.New("telemetry metrics are not in canonical order")
		}
		previous = metric.Key.String()
	}
	return nil
}

func EncodeDailyAggregate(aggregate DailyAggregate) ([]byte, error) {
	if err := aggregate.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(aggregate)
	if err != nil {
		return nil, fmt.Errorf("encode telemetry aggregate: %w", err)
	}
	return append(data, '\n'), nil
}

func DecodeDailyAggregate(data []byte) (DailyAggregate, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var aggregate DailyAggregate
	if err := decoder.Decode(&aggregate); err != nil {
		return DailyAggregate{}, fmt.Errorf("decode telemetry aggregate: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return DailyAggregate{}, errors.New("decode telemetry aggregate: trailing JSON value")
		}
		return DailyAggregate{}, fmt.Errorf("decode telemetry aggregate: %w", err)
	}
	if err := aggregate.Validate(); err != nil {
		return DailyAggregate{}, err
	}
	return aggregate, nil
}
