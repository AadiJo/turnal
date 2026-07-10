package analytics

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/AadiJo/turnal/internal/telemetry"
)

func TestSyntheticFixtureReconcilesDashboardMetrics(t *testing.T) {
	aggregates, expected, err := SyntheticFixture()
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := NewDataset(aggregates)
	if err != nil {
		t.Fatal(err)
	}
	asOf, _ := time.Parse(time.DateOnly, expected.AsOf)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if got := dataset.ActiveInstallations(start, asOf); got != expected.Installations {
		t.Fatalf("active installations = %d, want %d", got, expected.Installations)
	}
	if got := len(dataset.ActivationDates()); got != expected.Activated {
		t.Fatalf("activated installations = %d, want %d", got, expected.Activated)
	}
	if got := dataset.RecordingActive(asOf, 7); got != expected.WeeklyRecordingActive {
		t.Fatalf("weekly recording active = %d, want %d", got, expected.WeeklyRecordingActive)
	}
	d7, err := dataset.Retention(asOf, 7, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d7.Eligible != expected.D7Eligible || d7.Retained != expected.D7Retained {
		t.Fatalf("D7 retention = %#v", d7)
	}
	d30, err := dataset.Retention(asOf, 30, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d30.Eligible != expected.D30Eligible || d30.Retained != expected.D30Retained {
		t.Fatalf("D30 retention = %#v", d30)
	}
	search := dataset.FeatureAdoption(telemetry.MetricCommandSearchSuccess, asOf, 7)
	if search.RecordingActive != expected.WeeklyRecordingActive || search.FeatureActive != expected.SearchFeatureActive || !search.Visible || math.Abs(search.Share()-expected.SearchFeatureShare) > 1e-12 {
		t.Fatalf("search adoption = %#v", search)
	}
	status := dataset.CommandSuccessRate(
		telemetry.MetricCommandStatusSuccess,
		telemetry.MetricCommandStatusFailure,
		start,
		asOf,
	)
	if status.Success != expected.StatusSuccessCount || status.Failure != expected.StatusFailureCount || status.Executions() != 120 || status.Rate() != expected.StatusSuccessRate {
		t.Fatalf("status success = %#v", status)
	}
	if len(dataset.Records) != 73 {
		t.Fatalf("daily aggregate rows = %d, want 73; numeric status count must not use row count", len(dataset.Records))
	}
}

func TestDailySemanticsDoNotDependOnWithinDayOrder(t *testing.T) {
	aggregates, _, err := SyntheticFixture()
	if err != nil {
		t.Fatal(err)
	}
	forward, err := NewDataset(aggregates)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(aggregates)
	for index := range aggregates {
		slices.Reverse(aggregates[index].Metrics)
	}
	reversed, err := NewDataset(aggregates)
	if err == nil {
		t.Fatal("non-canonical metric order was accepted")
	}
	for index := range aggregates {
		slices.Reverse(aggregates[index].Metrics)
	}
	reversed, err = NewDataset(aggregates)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.ActivationDates()) != len(reversed.ActivationDates()) {
		t.Fatalf("activation changed with aggregate order")
	}
	for id, date := range forward.ActivationDates() {
		if !reversed.ActivationDates()[id].Equal(date) {
			t.Fatalf("activation date for %s changed", id)
		}
	}
}

func TestFeatureAdoptionSuppressesSmallSamples(t *testing.T) {
	aggregates, _, err := SyntheticFixture()
	if err != nil {
		t.Fatal(err)
	}
	allowed := make(map[string]struct{})
	var subset []telemetry.DailyAggregate
	for _, aggregate := range aggregates {
		id := aggregate.AnonymousID.String()
		if _, ok := allowed[id]; !ok && len(allowed) < 10 {
			allowed[id] = struct{}{}
		}
		if _, ok := allowed[id]; ok {
			subset = append(subset, aggregate)
		}
	}
	dataset, err := NewDataset(subset)
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	adoption := dataset.FeatureAdoption(telemetry.MetricCommandSearchSuccess, asOf, 7)
	if adoption.Visible || adoption.RecordingActive >= SmallSampleThreshold {
		t.Fatalf("small sample was visible: %#v", adoption)
	}
}

func TestDatasetRejectsDuplicateBatchAndInvalidRetention(t *testing.T) {
	aggregates, _, err := SyntheticFixture()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDataset([]telemetry.DailyAggregate{aggregates[0], aggregates[0]}); err == nil {
		t.Fatal("duplicate analytical batch accepted")
	}
	dataset, err := NewDataset(aggregates)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dataset.Retention(time.Now(), 0, 1); err == nil {
		t.Fatal("zero retention horizon accepted")
	}
}
