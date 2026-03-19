// @nexus-project: nexus
// @nexus-path: internal/upgrade/release.go
// Package upgrade implements the engx self-upgrade protocol (ADR-028).
// This file handles GitHub Releases API resolution.
package upgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
)

const (
	// ChannelStable targets the latest non-pre-release tag.
	ChannelStable = "stable"
	// ChannelBeta targets the latest release including pre-releases.
	ChannelBeta = "beta"

	releasesAPIURL = "https://api.github.com/repos/Harshmaury/Nexus/releases"
	userAgent      = "engx-upgrade/1.0"
)

// Release holds the resolved version and asset URLs for one GitHub release.
type Release struct {
	Version      string // e.g. "1.5.0"
	Tag          string // e.g. "v1.5.0"
	TarballURL   string // full download URL for the platform tarball
	ChecksumsURL string // full download URL for the checksums manifest
	IsPreRelease bool
}

// githubRelease is the minimal GitHub API release shape we need.
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// FetchLatest resolves the latest release for the given channel.
// Returns the Release with asset URLs built from the goreleaser naming
// convention (ADR-028).
func FetchLatest(ctx context.Context, channel string) (*Release, error) {
	releases, err := fetchReleases(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch releases: %w", err)
	}
	rel, err := pickRelease(releases, channel)
	if err != nil {
		return nil, err
	}
	return buildRelease(rel), nil
}

// fetchReleases fetches up to 20 recent releases from the GitHub API.
func fetchReleases(ctx context.Context) ([]githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPIURL+"?per_page=20", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET releases: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: HTTP %d", resp.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decode releases: %w", err)
	}
	return releases, nil
}

// pickRelease selects the best release for the channel.
func pickRelease(releases []githubRelease, channel string) (*githubRelease, error) {
	for i := range releases {
		r := &releases[i]
		if r.Draft {
			continue
		}
		if channel == ChannelStable && r.Prerelease {
			continue
		}
		return r, nil
	}
	return nil, fmt.Errorf("no %s release found", channel)
}

// buildRelease constructs a Release from a GitHub release entry.
func buildRelease(r *githubRelease) *Release {
	version := versionFromTag(r.TagName)
	os := runtime.GOOS
	arch := runtime.GOARCH
	base := fmt.Sprintf(
		"https://github.com/Harshmaury/Nexus/releases/download/%s/engx-%s",
		r.TagName, version,
	)
	return &Release{
		Version:      version,
		Tag:          r.TagName,
		TarballURL:   fmt.Sprintf("%s-%s-%s.tar.gz", base, os, arch),
		ChecksumsURL: fmt.Sprintf("%s-checksums.txt", base),
		IsPreRelease: r.Prerelease,
	}
}

// versionFromTag strips the leading "v" from a semver tag.
func versionFromTag(tag string) string {
	if len(tag) > 0 && tag[0] == 'v' {
		return tag[1:]
	}
	return tag
}
