package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryNoticeCache struct {
	entry NoticeCacheEntry
	err   error
	saved []NoticeCacheEntry
}

func (cache *memoryNoticeCache) Load() (NoticeCacheEntry, error) {
	if cache.err != nil {
		return NoticeCacheEntry{}, cache.err
	}
	return cache.entry, nil
}

func (cache *memoryNoticeCache) Save(entry NoticeCacheEntry) error {
	cache.saved = append(cache.saved, entry)
	cache.entry = entry
	cache.err = nil
	return nil
}

type countedRegistry struct {
	tags  map[string]string
	err   error
	calls int
}

func (registry *countedRegistry) DistTags(ctx context.Context) (map[string]string, error) {
	registry.calls++
	if registry.err != nil {
		return nil, registry.err
	}
	return registry.tags, nil
}

func (registry *countedRegistry) Version(ctx context.Context, npmTag string) (string, error) {
	registry.calls++
	if registry.err != nil {
		return "", registry.err
	}
	return registry.tags[npmTag], nil
}

func TestCheckUpdateNoticeStableUsesStableTagOnly(t *testing.T) {
	notice, ok, err := CheckUpdateNotice(context.Background(), NoticeOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{tags: map[string]string{
			"latest":  "0.4.2",
			"nightly": "0.4.3-nightly.20260709.4",
		}},
	})
	if err != nil {
		t.Fatalf("CheckUpdateNotice: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if notice.Target.Channel != ChannelStable || notice.Target.NPMTag != "latest" || notice.Target.Version != "0.4.2" {
		t.Fatalf("target = %+v, want stable latest 0.4.2", notice.Target)
	}
	if strings.Contains(notice.Message(), "nightly") {
		t.Fatalf("stable notice mentioned nightly:\n%s", notice.Message())
	}
}

func TestCheckUpdateNoticeNightlyUsesNightlyTag(t *testing.T) {
	notice, ok, err := CheckUpdateNotice(context.Background(), NoticeOptions{
		Current: Metadata{
			Version:       "0.4.3-nightly.20260708.12",
			Channel:       ChannelNightly,
			InstallSource: InstallSourceNPM,
		},
		Registry: fakeRegistry{tags: map[string]string{
			"latest":  "0.4.2",
			"nightly": "0.4.3-nightly.20260709.4",
		}},
	})
	if err != nil {
		t.Fatalf("CheckUpdateNotice: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if notice.Target.Channel != ChannelNightly || notice.Target.NPMTag != "nightly" || notice.Target.Version != "0.4.3-nightly.20260709.4" {
		t.Fatalf("target = %+v, want nightly tag", notice.Target)
	}
}

func TestCheckUpdateNoticeSkipsSourceInstalls(t *testing.T) {
	registry := &countedRegistry{tags: map[string]string{"latest": "0.4.2"}}
	_, ok, err := CheckUpdateNotice(context.Background(), NoticeOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceSource,
		},
		Registry: registry,
	})
	if err != nil {
		t.Fatalf("CheckUpdateNotice: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	if registry.calls != 0 {
		t.Fatalf("registry calls = %d, want 0", registry.calls)
	}
}

func TestCheckUpdateNoticeUsesFreshCache(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	cache := &memoryNoticeCache{
		entry: NoticeCacheEntry{
			CheckedAt:     now.Add(-time.Hour),
			Channel:       ChannelStable,
			NPMTag:        "latest",
			TargetVersion: "0.4.2",
		},
	}
	registry := &countedRegistry{err: errors.New("registry should not be called")}
	notice, ok, err := CheckUpdateNotice(context.Background(), NoticeOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: registry,
		Cache:    cache,
		Now:      now,
	})
	if err != nil {
		t.Fatalf("CheckUpdateNotice: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if notice.Target.Version != "0.4.2" {
		t.Fatalf("target version = %q, want 0.4.2", notice.Target.Version)
	}
	if registry.calls != 0 {
		t.Fatalf("registry calls = %d, want 0", registry.calls)
	}
}

func TestCheckUpdateNoticeSavesThrottleEntryOnLookupError(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	cache := &memoryNoticeCache{err: errors.New("missing cache")}
	_, ok, err := CheckUpdateNotice(context.Background(), NoticeOptions{
		Current: Metadata{
			Version:       "0.4.1",
			Channel:       ChannelStable,
			InstallSource: InstallSourceNPM,
		},
		Registry: &countedRegistry{err: errors.New("registry down")},
		Cache:    cache,
		Now:      now,
	})
	if err == nil {
		t.Fatal("CheckUpdateNotice succeeded, want registry error")
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	if len(cache.saved) != 1 {
		t.Fatalf("saved entries = %d, want 1", len(cache.saved))
	}
	if cache.saved[0].TargetVersion != "" || cache.saved[0].Channel != ChannelStable || cache.saved[0].NPMTag != "latest" {
		t.Fatalf("saved entry = %+v", cache.saved[0])
	}
}
