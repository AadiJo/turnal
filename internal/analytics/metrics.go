package analytics

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

const SmallSampleThreshold = 20

type DailyRecord struct {
	InstallationID string
	Date           time.Time
	Build          telemetry.Build
	Metrics        map[telemetry.MetricKey]uint64
}

type Dataset struct {
	Records []DailyRecord
}

type Retention struct {
	HorizonDays int
	WindowDays  int
	Eligible    int
	Retained    int
}

func (retention Retention) Rate() float64 {
	if retention.Eligible == 0 {
		return 0
	}
	return float64(retention.Retained) / float64(retention.Eligible)
}

type CommandRate struct {
	Success uint64
	Failure uint64
}

func (rate CommandRate) Executions() uint64 {
	return rate.Success + rate.Failure
}

func (rate CommandRate) Rate() float64 {
	if rate.Executions() == 0 {
		return 0
	}
	return float64(rate.Success) / float64(rate.Executions())
}

type Adoption struct {
	RecordingActive int
	FeatureActive   int
	Visible         bool
}

func (adoption Adoption) Share() float64 {
	if adoption.RecordingActive == 0 {
		return 0
	}
	return float64(adoption.FeatureActive) / float64(adoption.RecordingActive)
}

func NewDataset(aggregates []telemetry.DailyAggregate) (Dataset, error) {
	seenBatches := make(map[string]struct{}, len(aggregates))
	type grain struct {
		id    string
		date  string
		build telemetry.Build
	}
	records := make(map[grain]DailyRecord)
	for _, aggregate := range aggregates {
		if err := aggregate.Validate(); err != nil {
			return Dataset{}, err
		}
		if _, duplicate := seenBatches[aggregate.BatchID.String()]; duplicate {
			return Dataset{}, fmt.Errorf("duplicate analytical batch %s", aggregate.BatchID)
		}
		seenBatches[aggregate.BatchID.String()] = struct{}{}
		date, _ := time.Parse(time.DateOnly, aggregate.Date)
		key := grain{id: aggregate.AnonymousID.String(), date: aggregate.Date, build: aggregate.Build}
		record := records[key]
		if record.Metrics == nil {
			record = DailyRecord{
				InstallationID: aggregate.AnonymousID.String(),
				Date:           date,
				Build:          aggregate.Build,
				Metrics:        make(map[telemetry.MetricKey]uint64),
			}
		}
		for _, metric := range aggregate.Metrics {
			record.Metrics[metric.Key] += metric.Count
		}
		records[key] = record
	}
	dataset := Dataset{Records: make([]DailyRecord, 0, len(records))}
	for _, record := range records {
		dataset.Records = append(dataset.Records, record)
	}
	sort.Slice(dataset.Records, func(i, j int) bool {
		if dataset.Records[i].Date.Equal(dataset.Records[j].Date) {
			return dataset.Records[i].InstallationID < dataset.Records[j].InstallationID
		}
		return dataset.Records[i].Date.Before(dataset.Records[j].Date)
	})
	return dataset, nil
}

func (dataset Dataset) ActiveInstallations(start, end time.Time) int {
	active := make(map[string]struct{})
	for _, record := range dataset.Records {
		if dateInRange(record.Date, start, end) && len(record.Metrics) > 0 {
			active[record.InstallationID] = struct{}{}
		}
	}
	return len(active)
}

func (dataset Dataset) RecordingActive(end time.Time, days int) int {
	if days <= 0 {
		return 0
	}
	start := midnightUTC(end).AddDate(0, 0, -(days - 1))
	active := dataset.installationsWithAny(start, midnightUTC(end), recordingMetrics()...)
	return len(active)
}

func (dataset Dataset) InspectingActive(start, end time.Time) int {
	return len(dataset.installationsWithAny(start, end, inspectionMetrics()...))
}

func (dataset Dataset) RecoveryActive(start, end time.Time) int {
	return len(dataset.installationsWithAny(start, end, recoveryMetrics()...))
}

func (dataset Dataset) ActivationDates() map[string]time.Time {
	byInstallation := make(map[string][]DailyRecord)
	for _, record := range dataset.Records {
		byInstallation[record.InstallationID] = append(byInstallation[record.InstallationID], record)
	}
	activated := make(map[string]time.Time)
	for id, records := range byInstallation {
		var initializationDates []time.Time
		var valueDates []time.Time
		for _, record := range records {
			if record.Metrics[telemetry.MetricWorkspaceInitialized] > 0 {
				initializationDates = append(initializationDates, record.Date)
			}
			if hasAny(record.Metrics,
				telemetry.MetricAdapterConfiguredClaude,
				telemetry.MetricAdapterConfiguredCodex,
				telemetry.MetricCommandRunSuccess,
			) {
				valueDates = append(valueDates, record.Date)
			}
		}
		var earliest time.Time
		for _, initialized := range initializationDates {
			for _, valueDate := range valueDates {
				if valueDate.Before(initialized) {
					continue
				}
				activationDate := valueDate
				if activationDate.Before(initialized) {
					activationDate = initialized
				}
				if earliest.IsZero() || activationDate.Before(earliest) {
					earliest = activationDate
				}
			}
		}
		if !earliest.IsZero() {
			activated[id] = earliest
		}
	}
	return activated
}

func (dataset Dataset) Retention(asOf time.Time, horizonDays, windowDays int) (Retention, error) {
	if horizonDays <= 0 || windowDays <= 0 {
		return Retention{}, errors.New("retention horizon and window must be positive")
	}
	result := Retention{HorizonDays: horizonDays, WindowDays: windowDays}
	recordingDates := make(map[string][]time.Time)
	for _, record := range dataset.Records {
		if hasAny(record.Metrics, recordingMetrics()...) {
			recordingDates[record.InstallationID] = append(recordingDates[record.InstallationID], record.Date)
		}
	}
	for id, activationDate := range dataset.ActivationDates() {
		windowStart := activationDate.AddDate(0, 0, horizonDays)
		windowEnd := windowStart.AddDate(0, 0, windowDays-1)
		if windowEnd.After(midnightUTC(asOf)) {
			continue
		}
		result.Eligible++
		for _, recordDate := range recordingDates[id] {
			if dateInRange(recordDate, windowStart, windowEnd) {
				result.Retained++
				break
			}
		}
	}
	return result, nil
}

func (dataset Dataset) CommandSuccessRate(success, failure telemetry.MetricKey, start, end time.Time) CommandRate {
	var rate CommandRate
	for _, record := range dataset.Records {
		if !dateInRange(record.Date, start, end) {
			continue
		}
		rate.Success += record.Metrics[success]
		rate.Failure += record.Metrics[failure]
	}
	return rate
}

func (dataset Dataset) FeatureAdoption(feature telemetry.MetricKey, end time.Time, days int) Adoption {
	if days <= 0 {
		return Adoption{}
	}
	end = midnightUTC(end)
	start := end.AddDate(0, 0, -(days - 1))
	recording := dataset.installationsWithAny(start, end, recordingMetrics()...)
	featureInstallations := dataset.installationsWithAny(start, end, feature)
	numerator := 0
	for id := range featureInstallations {
		if _, ok := recording[id]; ok {
			numerator++
		}
	}
	return Adoption{
		RecordingActive: len(recording),
		FeatureActive:   numerator,
		Visible:         len(recording) >= SmallSampleThreshold,
	}
}

func (dataset Dataset) installationsWithAny(start, end time.Time, keys ...telemetry.MetricKey) map[string]struct{} {
	result := make(map[string]struct{})
	for _, record := range dataset.Records {
		if dateInRange(record.Date, start, end) && hasAny(record.Metrics, keys...) {
			result[record.InstallationID] = struct{}{}
		}
	}
	return result
}

func hasAny(metrics map[telemetry.MetricKey]uint64, keys ...telemetry.MetricKey) bool {
	for _, key := range keys {
		if metrics[key] > 0 {
			return true
		}
	}
	return false
}

func dateInRange(date, start, end time.Time) bool {
	date = midnightUTC(date)
	return !date.Before(midnightUTC(start)) && !date.After(midnightUTC(end))
}

func midnightUTC(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func recordingMetrics() []telemetry.MetricKey {
	return []telemetry.MetricKey{telemetry.MetricTurnRecordedClaude, telemetry.MetricTurnRecordedCodex}
}

func inspectionMetrics() []telemetry.MetricKey {
	return []telemetry.MetricKey{
		telemetry.MetricCommandLogSuccess,
		telemetry.MetricCommandSessionsSuccess,
		telemetry.MetricCommandShowSuccess,
		telemetry.MetricCommandSearchSuccess,
		telemetry.MetricCommandDiffSuccess,
		telemetry.MetricCommandBlameSuccess,
	}
}

func recoveryMetrics() []telemetry.MetricKey {
	return []telemetry.MetricKey{
		telemetry.MetricCommandRollbackSuccess,
		telemetry.MetricCommandReplayCheckoutSuccess,
		telemetry.MetricCommandReplayMoveSuccess,
		telemetry.MetricCommandReplayRemoveSuccess,
	}
}
