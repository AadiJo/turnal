package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

const PackageName = "@aadijo/turnal"

const (
	ChannelStable  = "stable"
	ChannelNightly = "nightly"
	ChannelDev     = "dev"

	InstallSourceNPM     = "npm"
	InstallSourceSource  = "source"
	InstallSourceUnknown = "unknown"

	ActionNone             = "none"
	ActionNPMInstallGlobal = "npm_install_global"
	ActionManual           = "manual"
)

type Metadata struct {
	Version       string `json:"version"`
	Channel       string `json:"channel"`
	Commit        string `json:"commit"`
	InstallSource string `json:"install_source"`
}

func (m Metadata) Normalize() Metadata {
	if m.Version == "" {
		m.Version = "0.0.0"
	}
	if m.Channel == "" {
		m.Channel = ChannelDev
	}
	if m.InstallSource == "" {
		m.InstallSource = InstallSourceUnknown
	}
	return m
}

type Current struct {
	Version       string `json:"version"`
	Channel       string `json:"channel"`
	InstallSource string `json:"install_source"`
}

type Target struct {
	Version string `json:"version"`
	Channel string `json:"channel"`
	NPMTag  string `json:"npm_tag"`
}

type Action struct {
	Kind                 string   `json:"kind"`
	Command              []string `json:"command,omitempty"`
	RequiresConfirmation bool     `json:"requires_confirmation"`
	Reasons              []string `json:"reasons,omitempty"`
}

type Plan struct {
	Current         Current `json:"current"`
	Target          Target  `json:"target"`
	Action          Action  `json:"action"`
	UpToDate        bool    `json:"up_to_date,omitempty"`
	UpdateAvailable bool    `json:"update_available,omitempty"`
}

type PlanOptions struct {
	Current          Metadata
	RequestedChannel string
	Registry         Registry
}

type Registry interface {
	DistTags(ctx context.Context) (map[string]string, error)
	Version(ctx context.Context, npmTag string) (string, error)
}

func BuildPlan(ctx context.Context, opts PlanOptions) (Plan, error) {
	current := opts.Current.Normalize()
	targetChannel, err := ResolveTargetChannel(current.Channel, opts.RequestedChannel)
	if err != nil {
		return Plan{}, err
	}
	npmTag, err := NPMTagForChannel(targetChannel)
	if err != nil {
		return Plan{}, err
	}
	if opts.Registry == nil {
		return Plan{}, fmt.Errorf("upgrade registry is required")
	}

	targetVersion, err := lookupTargetVersion(ctx, opts.Registry, npmTag)
	if err != nil {
		return Plan{}, err
	}

	comparison, err := CompareVersions(targetVersion, current.Version)
	if err != nil {
		return Plan{}, err
	}

	upToDate := comparison == 0 && current.Channel == targetChannel
	action := Action{Kind: ActionNone}
	command := NPMInstallCommand(npmTag)
	if !upToDate {
		if current.InstallSource == InstallSourceNPM {
			action.Kind = ActionNPMInstallGlobal
			action.Command = command
		} else {
			action.Kind = ActionManual
			action.Command = command
		}
	}

	action.Reasons = confirmationReasons(current, targetChannel, comparison, action.Kind)
	action.RequiresConfirmation = len(action.Reasons) > 0

	return Plan{
		Current: Current{
			Version:       current.Version,
			Channel:       current.Channel,
			InstallSource: current.InstallSource,
		},
		Target: Target{
			Version: targetVersion,
			Channel: targetChannel,
			NPMTag:  npmTag,
		},
		Action:          action,
		UpToDate:        upToDate,
		UpdateAvailable: comparison > 0,
	}, nil
}

func ResolveTargetChannel(currentChannel, requestedChannel string) (string, error) {
	switch requestedChannel {
	case "":
	case ChannelStable, ChannelNightly:
		return requestedChannel, nil
	default:
		return "", fmt.Errorf("unsupported channel %q", requestedChannel)
	}

	if currentChannel == ChannelNightly {
		return ChannelNightly, nil
	}
	return ChannelStable, nil
}

func NPMTagForChannel(channel string) (string, error) {
	switch channel {
	case ChannelStable:
		return "latest", nil
	case ChannelNightly:
		return "nightly", nil
	default:
		return "", fmt.Errorf("unsupported channel %q", channel)
	}
}

func NPMInstallCommand(npmTag string) []string {
	return []string{"npm", "install", "-g", PackageName + "@" + npmTag}
}

func lookupTargetVersion(ctx context.Context, registry Registry, npmTag string) (string, error) {
	tags, err := registry.DistTags(ctx)
	if err != nil {
		return "", err
	}
	tagVersion := strings.TrimSpace(tags[npmTag])
	version, err := registry.Version(ctx, npmTag)
	if err == nil {
		version = strings.TrimSpace(version)
		if version != "" {
			return version, nil
		}
	}
	if tagVersion != "" {
		return tagVersion, nil
	}
	if err != nil {
		return "", err
	}
	if version != "" {
		return version, nil
	}
	return "", fmt.Errorf("npm tag %q did not resolve to a version", npmTag)
}

func confirmationReasons(current Metadata, targetChannel string, comparison int, actionKind string) []string {
	var reasons []string
	if current.Channel != targetChannel && isReleaseChannel(current.Channel) {
		reasons = append(reasons, "channel_switch")
	}
	if comparison < 0 {
		reasons = append(reasons, "target_older")
	}
	if actionKind == ActionManual {
		reasons = append(reasons, "manual_install_source")
	}
	return reasons
}

func isReleaseChannel(channel string) bool {
	return channel == ChannelStable || channel == ChannelNightly
}

type NPMRegistry struct {
	Command string
}

func (r NPMRegistry) DistTags(ctx context.Context) (map[string]string, error) {
	output, err := r.run(ctx, "view", PackageName, "dist-tags", "--json")
	if err != nil {
		return nil, fmt.Errorf("query npm dist-tags: %w", err)
	}
	var tags map[string]string
	if err := json.Unmarshal(output, &tags); err != nil {
		return nil, fmt.Errorf("parse npm dist-tags: %w", err)
	}
	return tags, nil
}

func (r NPMRegistry) Version(ctx context.Context, npmTag string) (string, error) {
	output, err := r.run(ctx, "view", PackageName+"@"+npmTag, "version")
	if err != nil {
		return "", fmt.Errorf("query npm %s version: %w", npmTag, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func (r NPMRegistry) run(ctx context.Context, args ...string) ([]byte, error) {
	command := r.Command
	if command == "" {
		command = "npm"
	}
	cmd := exec.CommandContext(ctx, command, args...)
	return cmd.Output()
}

var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)

type parsedVersion struct {
	major int
	minor int
	patch int
	pre   []string
}

func CompareVersions(left, right string) (int, error) {
	a, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseVersion(right)
	if err != nil {
		return 0, err
	}

	for _, pair := range [][2]int{
		{a.major, b.major},
		{a.minor, b.minor},
		{a.patch, b.patch},
	} {
		switch {
		case pair[0] > pair[1]:
			return 1, nil
		case pair[0] < pair[1]:
			return -1, nil
		}
	}
	return comparePrerelease(a.pre, b.pre), nil
}

func parseVersion(value string) (parsedVersion, error) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return parsedVersion{}, fmt.Errorf("invalid version %q", value)
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	patch, _ := strconv.Atoi(match[3])
	var pre []string
	if match[4] != "" {
		pre = strings.Split(match[4], ".")
	}
	return parsedVersion{major: major, minor: minor, patch: patch, pre: pre}, nil
}

func comparePrerelease(left, right []string) int {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	if len(left) == 0 {
		return 1
	}
	if len(right) == 0 {
		return -1
	}

	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		comparison := comparePrereleaseIdentifier(left[i], right[i])
		if comparison != 0 {
			return comparison
		}
	}
	switch {
	case len(left) > len(right):
		return 1
	case len(left) < len(right):
		return -1
	default:
		return 0
	}
}

func comparePrereleaseIdentifier(left, right string) int {
	leftNumber, leftNumeric := numericPrereleaseIdentifier(left)
	rightNumber, rightNumeric := numericPrereleaseIdentifier(right)
	switch {
	case leftNumeric && rightNumeric:
		switch {
		case leftNumber > rightNumber:
			return 1
		case leftNumber < rightNumber:
			return -1
		default:
			return 0
		}
	case leftNumeric:
		return -1
	case rightNumeric:
		return 1
	default:
		return strings.Compare(left, right)
	}
}

func numericPrereleaseIdentifier(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return number, true
}
