package analytics

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPostHogDashboardDefinitionPreservesMetricSemantics(t *testing.T) {
	data, err := os.ReadFile("../../analytics/posthog/dashboard.json")
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		ScopeLabel string `json:"scope_label"`
		Source     struct {
			Timezone        string `json:"timezone"`
			CountRule       string `json:"count_rule"`
			OrderingRule    string `json:"ordering_rule"`
			GlobalPredicate string `json:"global_predicate"`
		} `json:"source"`
		SmallSample struct {
			Minimum int `json:"minimum_distinct_installations"`
		} `json:"small_sample"`
		Queries map[string]struct {
			Query string `json:"query"`
		} `json:"queries"`
	}
	if err := json.Unmarshal(data, &dashboard); err != nil {
		t.Fatal(err)
	}
	if dashboard.Source.Timezone != "UTC" || dashboard.SmallSample.Minimum != SmallSampleThreshold || !strings.Contains(strings.ToLower(dashboard.ScopeLabel), "consenting installations") {
		t.Fatalf("dashboard controls = %#v", dashboard)
	}
	if !strings.Contains(dashboard.Source.GlobalPredicate, "collector_canary") {
		t.Fatal("dashboard does not exclude synthetic collector canaries")
	}
	for _, required := range []string{
		"weekly_recording_active",
		"weekly_recording_active_trend",
		"activated_installations",
		"activation_conversion_by_version",
		"d7_recording_retention",
		"d30_recording_retention",
		"feature_adoption",
		"inspection_share",
		"recovery_share",
		"command_success_rate",
		"adapter_mix",
		"active_version_share",
	} {
		if strings.TrimSpace(dashboard.Queries[required].Query) == "" {
			t.Fatalf("missing dashboard query %q", required)
		}
	}
	for name, query := range dashboard.Queries {
		lower := strings.ToLower(query.Query)
		if strings.Contains(lower, " user") || strings.Contains(lower, "users") {
			t.Fatalf("query %s uses person terminology: %s", name, query.Query)
		}
		if strings.Contains(query.Query, "{") || strings.Contains(query.Query, "}") {
			t.Fatalf("query %s contains an unresolved placeholder: %s", name, query.Query)
		}
		if !strings.Contains(lower, "collector_canary") {
			t.Fatalf("query %s does not exclude collector canaries: %s", name, query.Query)
		}
		if name == "command_success_rate" || name == "adapter_mix" {
			if !strings.Contains(lower, "sum(") && !strings.Contains(lower, "sumif(") {
				t.Fatalf("count-aware query %s does not sum count: %s", name, query.Query)
			}
			if !strings.Contains(lower, "properties.count") {
				t.Fatalf("count-aware query %s ignores numeric count: %s", name, query.Query)
			}
		}
	}
	featureAdoption := strings.ToLower(dashboard.Queries["feature_adoption"].Query)
	if !strings.Contains(featureAdoption, "denominator") || !strings.Contains(featureAdoption, "cross join denominator") {
		t.Fatalf("feature adoption does not preserve the recording-active denominator: %s", featureAdoption)
	}
	activation := strings.ToLower(dashboard.Queries["activated_installations"].Query)
	if !strings.Contains(activation, "todate(properties.event_date)") || strings.Contains(activation, "order by") || strings.Contains(activation, "timestamp") {
		t.Fatalf("activation query relies on within-day order: %s", activation)
	}
}
