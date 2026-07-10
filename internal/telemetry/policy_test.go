package telemetry

import "testing"

func TestEvaluatePolicyRequiresExplicitOptIn(t *testing.T) {
	build := supportedTestBuild()
	for _, test := range []struct {
		name       string
		preference Preference
		want       PolicyResult
	}{
		{name: "unset", preference: PreferenceUnset, want: PolicyResult{Reason: ReasonNotOptedIn}},
		{name: "off", preference: PreferenceOff, want: PolicyResult{Reason: ReasonPreferenceOff}},
		{name: "on", preference: PreferenceOn, want: PolicyResult{Enabled: true, Reason: ReasonEnabled}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := EvaluatePolicy(PolicyOptions{Preference: test.preference, Build: build, LookupEnv: mapEnv(nil)})
			if got != test.want {
				t.Fatalf("EvaluatePolicy() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestEvaluatePolicyOverridesEnabledPreference(t *testing.T) {
	for _, test := range []struct {
		name  string
		build Build
		env   map[string]string
		want  PolicyReason
	}{
		{name: "Turnal opt out", build: supportedTestBuild(), env: map[string]string{"TURNAL_NO_ANALYTICS": "1"}, want: ReasonEnvironmentOptOut},
		{name: "do not track", build: supportedTestBuild(), env: map[string]string{"DO_NOT_TRACK": "true"}, want: ReasonEnvironmentOptOut},
		{name: "generic CI", build: supportedTestBuild(), env: map[string]string{"CI": "1"}, want: ReasonCI},
		{name: "provider CI", build: supportedTestBuild(), env: map[string]string{"GITHUB_ACTIONS": "true"}, want: ReasonCI},
		{name: "zero version", build: Build{Version: "0.0.0", Channel: ChannelStable, InstallSource: InstallSourceSource, OS: "linux", Arch: "amd64"}, want: ReasonDevelopmentBuild},
		{name: "dev channel", build: Build{Version: "0.4.2", Channel: "dev", InstallSource: InstallSourceSource, OS: "linux", Arch: "amd64"}, want: ReasonDevelopmentBuild},
		{name: "unsupported OS", build: Build{Version: "0.4.2", Channel: ChannelStable, InstallSource: InstallSourceNPM, OS: "plan9", Arch: "amd64"}, want: ReasonUnsupportedBuild},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := EvaluatePolicy(PolicyOptions{Preference: PreferenceOn, Build: test.build, LookupEnv: mapEnv(test.env)})
			if got.Enabled || got.Reason != test.want {
				t.Fatalf("EvaluatePolicy() = %#v, want disabled %s", got, test.want)
			}
		})
	}
}

func TestFalseEnvironmentValuesDoNotDisable(t *testing.T) {
	got := EvaluatePolicy(PolicyOptions{
		Preference: PreferenceOn,
		Build:      supportedTestBuild(),
		LookupEnv: mapEnv(map[string]string{
			"CI":                  "false",
			"DO_NOT_TRACK":        "0",
			"TURNAL_NO_ANALYTICS": "off",
		}),
	})
	if !got.Enabled {
		t.Fatalf("EvaluatePolicy() = %#v", got)
	}
}

func supportedTestBuild() Build {
	return Build{
		Version:       "0.4.2",
		Channel:       ChannelStable,
		InstallSource: InstallSourceNPM,
		OS:            "linux",
		Arch:          "amd64",
	}
}

func mapEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
