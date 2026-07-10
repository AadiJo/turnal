package analytics

import (
	"fmt"
	"sort"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

type SyntheticExpectation struct {
	AsOf                  string  `json:"as_of"`
	Installations         int     `json:"installations"`
	Activated             int     `json:"activated"`
	WeeklyRecordingActive int     `json:"weekly_recording_active"`
	D7Eligible            int     `json:"d7_eligible"`
	D7Retained            int     `json:"d7_retained"`
	D30Eligible           int     `json:"d30_eligible"`
	D30Retained           int     `json:"d30_retained"`
	SearchFeatureActive   int     `json:"search_feature_active"`
	SearchFeatureShare    float64 `json:"search_feature_share"`
	StatusSuccessCount    uint64  `json:"status_success_count"`
	StatusFailureCount    uint64  `json:"status_failure_count"`
	StatusSuccessRate     float64 `json:"status_success_rate"`
}

type dayMetrics map[int]map[telemetry.MetricKey]uint64

func SyntheticFixture() ([]telemetry.DailyAggregate, SyntheticExpectation, error) {
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	byInstallation := make([]dayMetrics, 30)
	for index := range byInstallation {
		byInstallation[index] = dayMetrics{
			0: {
				telemetry.MetricWorkspaceInitialized:    1,
				telemetry.MetricAdapterConfiguredClaude: 1,
				telemetry.MetricCommandStatusSuccess:    3,
				telemetry.MetricCommandStatusFailure:    1,
			},
		}
		if index < 24 {
			byInstallation[index][0][telemetry.MetricTurnRecordedClaude] = 1
		}
		if index < 21 {
			byInstallation[index][7] = map[telemetry.MetricKey]uint64{telemetry.MetricTurnRecordedClaude: 1}
		}
		if index < 18 {
			byInstallation[index][30] = map[telemetry.MetricKey]uint64{telemetry.MetricTurnRecordedClaude: 1}
		}
		if index >= 18 && index < 22 {
			byInstallation[index][33] = map[telemetry.MetricKey]uint64{telemetry.MetricTurnRecordedClaude: 1}
		}
		if index < 12 {
			ensureFixtureDay(byInstallation[index], 30)[telemetry.MetricCommandSearchSuccess] = 1
		}
		if index < 5 {
			ensureFixtureDay(byInstallation[index], 30)[telemetry.MetricCommandRollbackSuccess] = 1
		}
	}
	build := telemetry.Build{
		Version:       "0.4.2",
		Channel:       telemetry.ChannelStable,
		InstallSource: telemetry.InstallSourceNPM,
		OS:            "linux",
		Arch:          "amd64",
	}
	var aggregates []telemetry.DailyAggregate
	for index, days := range byInstallation {
		id, err := telemetry.ParseUUID(fmt.Sprintf("%08x-0000-4000-a000-%012x", index+1, index+1))
		if err != nil {
			return nil, SyntheticExpectation{}, err
		}
		dayNumbers := make([]int, 0, len(days))
		for day := range days {
			dayNumbers = append(dayNumbers, day)
		}
		sort.Ints(dayNumbers)
		for _, day := range dayNumbers {
			aggregate, err := telemetry.NewDailyAggregate(id, base.AddDate(0, 0, day), build, days[day])
			if err != nil {
				return nil, SyntheticExpectation{}, err
			}
			aggregates = append(aggregates, aggregate)
		}
	}
	return aggregates, SyntheticExpectation{
		AsOf:                  "2026-07-05",
		Installations:         30,
		Activated:             30,
		WeeklyRecordingActive: 22,
		D7Eligible:            30,
		D7Retained:            21,
		D30Eligible:           30,
		D30Retained:           18,
		SearchFeatureActive:   12,
		SearchFeatureShare:    12.0 / 22.0,
		StatusSuccessCount:    90,
		StatusFailureCount:    30,
		StatusSuccessRate:     0.75,
	}, nil
}

func ensureFixtureDay(days dayMetrics, day int) map[telemetry.MetricKey]uint64 {
	metrics := days[day]
	if metrics == nil {
		metrics = make(map[telemetry.MetricKey]uint64)
		days[day] = metrics
	}
	return metrics
}
