package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultNoticeInterval = 24 * time.Hour

type UpdateNotice struct {
	Current Current  `json:"current"`
	Target  Target   `json:"target"`
	Command []string `json:"command"`
}

func (notice UpdateNotice) Message() string {
	return fmt.Sprintf("Turnal %s is available on %s. You have %s.\nRun `turnal upgrade` to update.", notice.Target.Version, notice.Target.Channel, notice.Current.Version)
}

type NoticeOptions struct {
	Current  Metadata
	Registry Registry
	Cache    NoticeCache
	Now      time.Time
	Interval time.Duration
}

type NoticeCache interface {
	Load() (NoticeCacheEntry, error)
	Save(NoticeCacheEntry) error
}

type NoticeCacheEntry struct {
	CheckedAt     time.Time `json:"checked_at"`
	Channel       string    `json:"channel"`
	NPMTag        string    `json:"npm_tag"`
	TargetVersion string    `json:"target_version,omitempty"`
}

func CheckUpdateNotice(ctx context.Context, opts NoticeOptions) (UpdateNotice, bool, error) {
	current := opts.Current.Normalize()
	if current.InstallSource != InstallSourceNPM && current.InstallSource != InstallSourceStandalone {
		return UpdateNotice{}, false, nil
	}
	if current.Channel != ChannelStable && current.Channel != ChannelNightly {
		return UpdateNotice{}, false, nil
	}

	npmTag, err := NPMTagForChannel(current.Channel)
	if err != nil {
		return UpdateNotice{}, false, err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultNoticeInterval
	}

	if opts.Cache != nil {
		entry, err := opts.Cache.Load()
		if err == nil && entryFreshForChannel(entry, now, interval, current.Channel, npmTag) {
			return noticeFromCachedTarget(current, entry)
		}
	}
	if opts.Registry == nil {
		return UpdateNotice{}, false, fmt.Errorf("upgrade registry is required")
	}

	entry := NoticeCacheEntry{
		CheckedAt: now,
		Channel:   current.Channel,
		NPMTag:    npmTag,
	}
	targetVersion, err := lookupTargetVersion(ctx, opts.Registry, npmTag)
	if err == nil {
		entry.TargetVersion = targetVersion
	}
	if opts.Cache != nil {
		_ = opts.Cache.Save(entry)
	}
	if err != nil {
		return UpdateNotice{}, false, err
	}
	return noticeFromTargetVersion(current, targetVersion, npmTag)
}

func entryFreshForChannel(entry NoticeCacheEntry, now time.Time, interval time.Duration, channel string, npmTag string) bool {
	if entry.Channel != channel || entry.NPMTag != npmTag || entry.CheckedAt.IsZero() {
		return false
	}
	if now.Before(entry.CheckedAt) {
		return true
	}
	return now.Sub(entry.CheckedAt) < interval
}

func noticeFromCachedTarget(current Metadata, entry NoticeCacheEntry) (UpdateNotice, bool, error) {
	if strings.TrimSpace(entry.TargetVersion) == "" {
		return UpdateNotice{}, false, nil
	}
	return noticeFromTargetVersion(current, entry.TargetVersion, entry.NPMTag)
}

func noticeFromTargetVersion(current Metadata, targetVersion string, npmTag string) (UpdateNotice, bool, error) {
	comparison, err := CompareVersions(targetVersion, current.Version)
	if err != nil {
		return UpdateNotice{}, false, err
	}
	if comparison <= 0 {
		return UpdateNotice{}, false, nil
	}
	command := NPMInstallCommand(npmTag)
	if current.InstallSource == InstallSourceStandalone {
		command = []string{"turnal", "upgrade"}
	}
	return UpdateNotice{
		Current: Current{
			Version:       current.Version,
			Channel:       current.Channel,
			InstallSource: current.InstallSource,
		},
		Target: Target{
			Version: targetVersion,
			Channel: current.Channel,
			NPMTag:  npmTag,
		},
		Command: command,
	}, true, nil
}

type FileNoticeCache struct {
	Path string
}

func DefaultNoticeCachePath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "turnal", "update-check.json"), nil
}

func (cache FileNoticeCache) Load() (NoticeCacheEntry, error) {
	if cache.Path == "" {
		return NoticeCacheEntry{}, errors.New("notice cache path is empty")
	}
	data, err := os.ReadFile(cache.Path)
	if err != nil {
		return NoticeCacheEntry{}, err
	}
	var entry NoticeCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return NoticeCacheEntry{}, err
	}
	return entry, nil
}

func (cache FileNoticeCache) Save(entry NoticeCacheEntry) error {
	if cache.Path == "" {
		return errors.New("notice cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(cache.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(cache.Path, data, 0o644)
}
