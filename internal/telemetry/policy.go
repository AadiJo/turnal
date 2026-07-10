package telemetry

import (
	"os"
	"strings"
)

type Preference string

const (
	PreferenceUnset Preference = ""
	PreferenceOff   Preference = "off"
	PreferenceOn    Preference = "on"
)

func (preference Preference) Valid() bool {
	return preference == PreferenceUnset || preference == PreferenceOff || preference == PreferenceOn
}

type PolicyReason string

const (
	ReasonEnabled           PolicyReason = "enabled"
	ReasonNotOptedIn        PolicyReason = "not_opted_in"
	ReasonPreferenceOff     PolicyReason = "preference_off"
	ReasonEnvironmentOptOut PolicyReason = "environment_opt_out"
	ReasonCI                PolicyReason = "ci"
	ReasonDevelopmentBuild  PolicyReason = "development_build"
	ReasonUnsupportedBuild  PolicyReason = "unsupported_build"
)

type PolicyResult struct {
	Enabled bool
	Reason  PolicyReason
}

type PolicyOptions struct {
	Preference Preference
	Build      Build
	LookupEnv  func(string) (string, bool)
}

func EvaluatePolicy(options PolicyOptions) PolicyResult {
	lookupEnv := options.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	if envTruthy(lookupEnv, "TURNAL_NO_ANALYTICS") || envTruthy(lookupEnv, "DO_NOT_TRACK") {
		return PolicyResult{Reason: ReasonEnvironmentOptOut}
	}
	if isCI(lookupEnv) {
		return PolicyResult{Reason: ReasonCI}
	}
	if strings.TrimSpace(options.Build.Version) == "0.0.0" || options.Build.Channel == "dev" {
		return PolicyResult{Reason: ReasonDevelopmentBuild}
	}
	if err := options.Build.Validate(); err != nil {
		return PolicyResult{Reason: ReasonUnsupportedBuild}
	}
	if !options.Preference.Valid() || options.Preference == PreferenceUnset {
		return PolicyResult{Reason: ReasonNotOptedIn}
	}
	if options.Preference == PreferenceOff {
		return PolicyResult{Reason: ReasonPreferenceOff}
	}
	return PolicyResult{Enabled: true, Reason: ReasonEnabled}
}

func envTruthy(lookupEnv func(string) (string, bool), name string) bool {
	value, ok := lookupEnv(name)
	if !ok {
		return false
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "0" && value != "false" && value != "no" && value != "off"
}

func isCI(lookupEnv func(string) (string, bool)) bool {
	for _, name := range []string{
		"CI",
		"CONTINUOUS_INTEGRATION",
		"GITHUB_ACTIONS",
		"GITLAB_CI",
		"BUILDKITE",
		"CIRCLECI",
		"JENKINS_URL",
		"TF_BUILD",
	} {
		if envTruthy(lookupEnv, name) {
			return true
		}
	}
	return false
}
