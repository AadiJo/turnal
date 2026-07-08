package upgrade

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeRegistry struct {
	tags     map[string]string
	versions map[string]string
	err      error
}

func (r fakeRegistry) DistTags(ctx context.Context) (map[string]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.tags, nil
}

func (r fakeRegistry) Version(ctx context.Context, npmTag string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.versions[npmTag], nil
}

func TestBuildPlanStableStaysStable(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{tags: map[string]string{"latest": "0.4.2"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if plan.Target.Channel != ChannelStable || plan.Target.NPMTag != "latest" || plan.Target.Version != "0.4.2" {
		t.Fatalf("target = %+v, want stable latest 0.4.2", plan.Target)
	}
	if plan.Action.Kind != ActionNPMInstallGlobal {
		t.Fatalf("action kind = %q, want %q", plan.Action.Kind, ActionNPMInstallGlobal)
	}
	if plan.Action.RequiresConfirmation {
		t.Fatalf("requires confirmation for same-channel upgrade: %+v", plan.Action)
	}
	if !reflect.DeepEqual(plan.Action.Command, []string{"npm", "install", "-g", "@aadijo/turnal@latest"}) {
		t.Fatalf("command = %#v", plan.Action.Command)
	}
	if !plan.UpdateAvailable {
		t.Fatal("UpdateAvailable = false, want true")
	}
}

func TestBuildPlanNightlyStaysNightly(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.3-nightly.20260708.12",
			Channel:       ChannelNightly,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{tags: map[string]string{"nightly": "0.4.3-nightly.20260709.4"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if plan.Target.Channel != ChannelNightly || plan.Target.NPMTag != "nightly" {
		t.Fatalf("target = %+v, want nightly tag", plan.Target)
	}
	if plan.Action.RequiresConfirmation {
		t.Fatalf("requires confirmation for same-channel nightly upgrade: %+v", plan.Action)
	}
	if !reflect.DeepEqual(plan.Action.Command, []string{"npm", "install", "-g", "@aadijo/turnal@nightly"}) {
		t.Fatalf("command = %#v", plan.Action.Command)
	}
}

func TestBuildPlanStableToNightlyRequiresConfirmation(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.2",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		RequestedChannel: ChannelNightly,
		Registry:         fakeRegistry{tags: map[string]string{"nightly": "0.4.3-nightly.20260709.4"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if !plan.Action.RequiresConfirmation {
		t.Fatal("RequiresConfirmation = false, want true")
	}
	if !hasReason(plan.Action, "channel_switch") {
		t.Fatalf("reasons = %#v, want channel_switch", plan.Action.Reasons)
	}
	if hasReason(plan.Action, "target_older") {
		t.Fatalf("reasons = %#v, did not want target_older", plan.Action.Reasons)
	}
}

func TestBuildPlanNightlyToStableOlderRequiresConfirmation(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.3-nightly.20260708.12",
			Channel:       ChannelNightly,
			InstallSource: InstallSourceNPM,
		},
		RequestedChannel: ChannelStable,
		Registry:         fakeRegistry{tags: map[string]string{"latest": "0.4.2"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if !plan.Action.RequiresConfirmation {
		t.Fatal("RequiresConfirmation = false, want true")
	}
	for _, reason := range []string{"channel_switch", "target_older"} {
		if !hasReason(plan.Action, reason) {
			t.Fatalf("reasons = %#v, want %s", plan.Action.Reasons, reason)
		}
	}
	if plan.UpdateAvailable {
		t.Fatal("UpdateAvailable = true, want false for older stable target")
	}
}

func TestBuildPlanUnknownInstallSourceIsManual(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.2",
			Channel:       ChannelStable,
			InstallSource: InstallSourceUnknown,
		},
		Registry: fakeRegistry{tags: map[string]string{"latest": "0.4.3"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if plan.Action.Kind != ActionManual {
		t.Fatalf("action kind = %q, want %q", plan.Action.Kind, ActionManual)
	}
	if !hasReason(plan.Action, "manual_install_source") {
		t.Fatalf("reasons = %#v, want manual_install_source", plan.Action.Reasons)
	}
}

func TestBuildPlanUpToDateHasNoAction(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.2",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{tags: map[string]string{"latest": "0.4.2"}},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}

	if !plan.UpToDate {
		t.Fatal("UpToDate = false, want true")
	}
	if plan.Action.Kind != ActionNone {
		t.Fatalf("action kind = %q, want %q", plan.Action.Kind, ActionNone)
	}
}

func TestBuildPlanFallsBackToTagVersionLookup(t *testing.T) {
	plan, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{
			tags:     map[string]string{},
			versions: map[string]string{"latest": "0.4.2"},
		},
	})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Target.Version != "0.4.2" {
		t.Fatalf("target version = %q, want 0.4.2", plan.Target.Version)
	}
}

func TestBuildPlanPropagatesRegistryError(t *testing.T) {
	_, err := BuildPlan(context.Background(), PlanOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{err: errors.New("registry down")},
	})
	if err == nil {
		t.Fatal("BuildPlan succeeded, want error")
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		want  int
	}{
		{name: "newer patch", left: "0.4.2", right: "0.4.1", want: 1},
		{name: "older stable than newer nightly core", left: "0.4.2", right: "0.4.3-nightly.20260708.12", want: -1},
		{name: "newer nightly date", left: "0.4.3-nightly.20260709.4", right: "0.4.3-nightly.20260708.12", want: 1},
		{name: "newer nightly run", left: "0.4.3-nightly.20260709.5", right: "0.4.3-nightly.20260709.4", want: 1},
		{name: "stable beats prerelease same core", left: "0.4.3", right: "0.4.3-nightly.20260709.4", want: 1},
		{name: "equal", left: "0.4.2", right: "0.4.2", want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CompareVersions(test.left, test.right)
			if err != nil {
				t.Fatalf("CompareVersions: %v", err)
			}
			if got != test.want {
				t.Fatalf("CompareVersions(%q, %q) = %d, want %d", test.left, test.right, got, test.want)
			}
		})
	}
}

func hasReason(action Action, reason string) bool {
	for _, candidate := range action.Reasons {
		if candidate == reason {
			return true
		}
	}
	return false
}
