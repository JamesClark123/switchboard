// Package update is the shared self-update core for both binaries: it looks up
// releases on GitHub, downloads a release archive, verifies it against the
// release's SHA-256 checksums, extracts a named binary, and atomically replaces
// an on-disk executable. The daemon uses it to update itself in place; the TUI
// uses it to update the local binaries and drives remote daemons via RPC.
//
// The release asset layout MUST match the GoReleaser configuration: archives are
// named "switchboard_<os>_<arch>.tar.gz" and a "checksums.txt" (sha256) sits
// alongside them in the same release.
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/minio/selfupdate"
)

const (
	// RepoOwner/RepoName identify the canonical GitHub repository releases are
	// pulled from. ProjectName is the GoReleaser project name and the archive
	// filename prefix.
	RepoOwner   = "jamesclark123"
	RepoName    = "switchboard"
	ProjectName = "switchboard"

	// ChecksumsAsset is the release asset holding SHA-256 sums (GoReleaser default).
	ChecksumsAsset = "checksums.txt"
)

// apiBase and httpClient are package-level so tests can point them at a local
// server; production uses the real GitHub API over HTTPS.
var (
	apiBase    = "https://api.github.com"
	httpClient = &http.Client{Timeout: 60 * time.Second}
)

// Release is the subset of a GitHub release the updater needs.
type Release struct {
	Version string // the release tag, e.g. "v0.4.1"
	assets  map[string]string
}

// AssetName is the archive filename for a given platform. It MUST match the
// GoReleaser `archives.name_template` ("{{.ProjectName}}_{{.Os}}_{{.Arch}}").
func AssetName(goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s.tar.gz", ProjectName, goos, goarch)
}

// AssetURL returns the download URL of the platform archive within this release.
func (r *Release) AssetURL(goos, goarch string) (string, bool) {
	u, ok := r.assets[AssetName(goos, goarch)]
	return u, ok
}

// ChecksumsURL returns the download URL of the release's checksums.txt.
func (r *Release) ChecksumsURL() (string, bool) {
	u, ok := r.assets[ChecksumsAsset]
	return u, ok
}

// LatestRelease resolves the most recent published release.
func LatestRelease(ctx context.Context) (*Release, error) {
	return fetchRelease(ctx, fmt.Sprintf("%s/repos/%s/%s/releases/latest", apiBase, RepoOwner, RepoName))
}

// ReleaseByTag resolves a specific release by its tag, e.g. "v0.4.1". Passing an
// empty tag falls back to the latest release so callers can treat "" as "latest".
func ReleaseByTag(ctx context.Context, tag string) (*Release, error) {
	if tag == "" {
		return LatestRelease(ctx)
	}
	return fetchRelease(ctx, fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", apiBase, RepoOwner, RepoName, tag))
}

func fetchRelease(ctx context.Context, url string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github release lookup: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github release lookup %s: unexpected status %s", url, resp.Status)
	}
	var raw struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name        string `json:"name"`
			DownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	rel := &Release{Version: raw.TagName, assets: make(map[string]string, len(raw.Assets))}
	for _, a := range raw.Assets {
		rel.assets[a.Name] = a.DownloadURL
	}
	return rel, nil
}

// Fetch downloads the platform archive and the checksums file, then verifies the
// archive's SHA-256 against the listed sum. It returns the archive bytes only
// when verification passes — callers MUST NOT swap any binary on error.
func Fetch(ctx context.Context, assetURL, checksumsURL, assetName string) ([]byte, error) {
	archive, err := download(ctx, assetURL)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", assetName, err)
	}
	sums, err := download(ctx, checksumsURL)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want, ok := checksumFor(sums, assetName)
	if !ok {
		return nil, fmt.Errorf("%s has no entry for %s", ChecksumsAsset, assetName)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("checksum mismatch for %s: got %s, want %s", assetName, hex.EncodeToString(got[:]), want)
	}
	return archive, nil
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// checksumFor finds the hex sum for assetName in a GNU coreutils-style
// checksums file ("<hex>  <name>", optionally "<hex> *<name>" for binary mode).
func checksumFor(sums []byte, assetName string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if path.Base(name) == assetName {
			return fields[0], true
		}
	}
	return "", false
}

// FetchBinary resolves a release (empty targetVersion = latest), downloads and
// SHA-256-verifies the platform archive, and extracts the named binary. It is
// the one-call path both the daemon (self-update) and the TUI (local swap) use.
func FetchBinary(ctx context.Context, targetVersion, goos, goarch, binaryName string) (version string, binary []byte, err error) {
	rel, err := ReleaseByTag(ctx, targetVersion)
	if err != nil {
		return "", nil, err
	}
	assetURL, ok := rel.AssetURL(goos, goarch)
	if !ok {
		return "", nil, fmt.Errorf("release %s has no build for %s/%s", rel.Version, goos, goarch)
	}
	sumsURL, ok := rel.ChecksumsURL()
	if !ok {
		return "", nil, fmt.Errorf("release %s has no %s", rel.Version, ChecksumsAsset)
	}
	archive, err := Fetch(ctx, assetURL, sumsURL, AssetName(goos, goarch))
	if err != nil {
		return "", nil, err
	}
	bin, err := ExtractBinary(archive, binaryName)
	if err != nil {
		return "", nil, err
	}
	return rel.Version, bin, nil
}

// ExtractBinary returns the bytes of the regular file whose base name is `name`
// from a gzip-compressed tar archive.
func ExtractBinary(targz []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(targz))
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && path.Base(hdr.Name) == name {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %q not found in archive", name)
}

// ApplyToPath atomically replaces the executable at `path` with binaryBytes.
func ApplyToPath(binaryBytes []byte, path string) error {
	return selfupdate.Apply(bytes.NewReader(binaryBytes), selfupdate.Options{TargetPath: path})
}

// ApplyToSelf atomically replaces the currently-running executable.
func ApplyToSelf(binaryBytes []byte) error {
	return selfupdate.Apply(bytes.NewReader(binaryBytes), selfupdate.Options{})
}

// IsBrewManaged reports whether execPath resolves under a Homebrew prefix, in
// which case the self-updater must defer to `brew upgrade` rather than rewrite a
// file Homebrew owns. Homebrew's bin entries are symlinks into the Cellar, so we
// resolve symlinks before matching the known prefixes.
func IsBrewManaged(execPath string) bool {
	resolved := execPath
	if p, err := filepath.EvalSymlinks(execPath); err == nil {
		resolved = p
	}
	for _, prefix := range []string{"/opt/homebrew/", "/usr/local/Cellar/", "/home/linuxbrew/.linuxbrew/"} {
		if strings.HasPrefix(resolved, prefix) {
			return true
		}
	}
	return false
}

// SemverNewer reports whether version a is strictly newer than b, tolerating a
// leading "v" and ignoring any pre-release/build suffix ("-dev", "+meta").
func SemverNewer(a, b string) bool {
	return compareSemver(a, b) > 0
}

func compareSemver(a, b string) int {
	pa, pb := parseSemver(a), parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func parseSemver(s string) [3]int {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(s, ".", 3) {
		if i > 2 {
			break
		}
		out[i], _ = strconv.Atoi(part)
	}
	return out
}
