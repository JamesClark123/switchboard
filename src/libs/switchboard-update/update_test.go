package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz builds a gzip-compressed tar containing files{name:content}.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeGitHub serves release metadata + assets and wires apiBase at it.
func fakeGitHub(t *testing.T, archive []byte, checksums string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	release := func(tag string) string {
		return fmt.Sprintf(`{"tag_name":%q,"assets":[
			{"name":%q,"browser_download_url":%q},
			{"name":"checksums.txt","browser_download_url":%q}
		]}`, tag, AssetName("linux", "amd64"), srv.URL+"/dl/asset", srv.URL+"/dl/sums")
	}
	mux.HandleFunc("/repos/"+RepoOwner+"/"+RepoName+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(release("v1.2.3")))
	})
	mux.HandleFunc("/repos/"+RepoOwner+"/"+RepoName+"/releases/tags/v1.2.3", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(release("v1.2.3")))
	})
	mux.HandleFunc("/repos/"+RepoOwner+"/"+RepoName+"/releases/tags/missing", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/bad-json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	mux.HandleFunc("/dl/asset", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(checksums)) })

	orig := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = orig; srv.Close() })
	return srv
}

// fakeGitHubNoChecksums serves a release whose assets omit checksums.txt.
func fakeGitHubNoChecksums(t *testing.T, archive []byte) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/repos/"+RepoOwner+"/"+RepoName+"/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q,"browser_download_url":%q}]}`,
			AssetName("linux", "amd64"), srv.URL+"/dl/asset")))
	})
	mux.HandleFunc("/dl/asset", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	orig := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = orig; srv.Close() })
}

// TestAssetNameMatchesGoReleaser pins the archive-name contract shared by the
// producer (.goreleaser.yaml archives.name_template) and both consumers
// (install.sh, this updater). Any drift here breaks installs AND in-app updates
// simultaneously — see specs/002-release-channel/contracts/release-assets.md
// (FR-006, FR-017). The whole {darwin,linux}×{amd64,arm64} matrix is asserted so
// a name-template change to any platform fails loudly.
func TestAssetNameMatchesGoReleaser(t *testing.T) {
	want := map[[2]string]string{
		{"darwin", "arm64"}: "switchboard_darwin_arm64.tar.gz",
		{"darwin", "amd64"}: "switchboard_darwin_amd64.tar.gz",
		{"linux", "arm64"}:  "switchboard_linux_arm64.tar.gz",
		{"linux", "amd64"}:  "switchboard_linux_amd64.tar.gz",
	}
	for k, exp := range want {
		if got := AssetName(k[0], k[1]); got != exp {
			t.Errorf("AssetName(%q,%q) = %q, want %q", k[0], k[1], got, exp)
		}
	}
}

func TestReleaseLookupAndURLs(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"sxbd": "DAEMON", "sxb": "TUI"})
	sums := sha256hex(archive) + "  " + AssetName("linux", "amd64") + "\n"
	fakeGitHub(t, archive, sums)

	ctx := context.Background()
	rel, err := LatestRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "v1.2.3" {
		t.Errorf("Version = %q", rel.Version)
	}
	if _, ok := rel.AssetURL("linux", "amd64"); !ok {
		t.Error("expected an asset URL for linux/amd64")
	}
	if _, ok := rel.AssetURL("plan9", "s390x"); ok {
		t.Error("unexpected asset URL for an unbuilt platform")
	}
	if _, ok := rel.ChecksumsURL(); !ok {
		t.Error("expected a checksums URL")
	}

	// Empty tag falls back to latest.
	if _, err := ReleaseByTag(ctx, ""); err != nil {
		t.Errorf("ReleaseByTag(\"\") should fall back to latest: %v", err)
	}
	if _, err := ReleaseByTag(ctx, "v1.2.3"); err != nil {
		t.Errorf("ReleaseByTag(tag): %v", err)
	}
	// Non-200 surfaces an error.
	if _, err := ReleaseByTag(ctx, "missing"); err == nil {
		t.Error("expected error for a missing tag")
	}
}

func TestFetchVerifiesChecksum(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"sxbd": "DAEMON"})
	asset := AssetName("linux", "amd64")
	sums := sha256hex(archive) + "  " + asset + "\n"
	srv := fakeGitHub(t, archive, sums)
	ctx := context.Background()

	rel, err := LatestRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assetURL, _ := rel.AssetURL("linux", "amd64")
	sumsURL, _ := rel.ChecksumsURL()

	got, err := Fetch(ctx, assetURL, sumsURL, asset)
	if err != nil {
		t.Fatalf("Fetch (valid): %v", err)
	}
	if !bytes.Equal(got, archive) {
		t.Error("Fetch returned altered bytes")
	}

	// Checksum mismatch must abort.
	bad := fakeGitHub(t, archive, "deadbeef  "+asset+"\n")
	relBad, _ := LatestRelease(ctx)
	aURL, _ := relBad.AssetURL("linux", "amd64")
	sURL, _ := relBad.ChecksumsURL()
	if _, err := Fetch(ctx, aURL, sURL, asset); err == nil {
		t.Error("expected checksum-mismatch error")
	}
	_ = srv
	_ = bad

	// Missing entry for the asset.
	fakeGitHub(t, archive, "abc  some-other-file\n")
	rel2, _ := LatestRelease(ctx)
	a2, _ := rel2.AssetURL("linux", "amd64")
	s2, _ := rel2.ChecksumsURL()
	if _, err := Fetch(ctx, a2, s2, asset); err == nil {
		t.Error("expected missing-entry error")
	}

	// Download error (bad URL).
	if _, err := Fetch(ctx, "http://127.0.0.1:0/nope", s2, asset); err == nil {
		t.Error("expected download error for asset")
	}
	if _, err := Fetch(ctx, a2, "http://127.0.0.1:0/nope", asset); err == nil {
		t.Error("expected download error for checksums")
	}
}

func TestFetchBinary(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"sxbd": "DAEMON", "sxb": "TUI"})
	sums := sha256hex(archive) + "  " + AssetName("linux", "amd64") + "\n"
	fakeGitHub(t, archive, sums)
	ctx := context.Background()

	ver, bin, err := FetchBinary(ctx, "", "linux", "amd64", "sxbd")
	if err != nil {
		t.Fatalf("FetchBinary: %v", err)
	}
	if ver != "v1.2.3" || string(bin) != "DAEMON" {
		t.Errorf("FetchBinary = %q,%q", ver, bin)
	}

	// No build for this platform.
	if _, _, err := FetchBinary(ctx, "", "plan9", "s390x", "sxbd"); err == nil {
		t.Error("expected no-build-for-platform error")
	}
	// Missing checksums asset in the release.
	fakeGitHubNoChecksums(t, archive)
	if _, _, err := FetchBinary(ctx, "", "linux", "amd64", "sxbd"); err == nil {
		t.Error("expected missing-checksums error")
	}
}

func TestChecksumForFormats(t *testing.T) {
	sums := "  \n" + // blank/short line skipped
		"11aa  file-a.tar.gz\n" +
		"22bb *file-b.tar.gz\n" // binary-mode marker
	if sum, ok := checksumFor([]byte(sums), "file-a.tar.gz"); !ok || sum != "11aa" {
		t.Errorf("file-a: got %q,%v", sum, ok)
	}
	if sum, ok := checksumFor([]byte(sums), "file-b.tar.gz"); !ok || sum != "22bb" {
		t.Errorf("file-b (binary mode): got %q,%v", sum, ok)
	}
	if _, ok := checksumFor([]byte(sums), "absent.tar.gz"); ok {
		t.Error("absent entry should not be found")
	}
}

func TestExtractBinary(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"sxbd": "DAEMONBYTES", "sxb": "TUIBYTES"})
	got, err := ExtractBinary(archive, "sxbd")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "DAEMONBYTES" {
		t.Errorf("extracted %q", got)
	}
	if _, err := ExtractBinary(archive, "nope"); err == nil {
		t.Error("expected not-found error")
	}
	if _, err := ExtractBinary([]byte("not gzip"), "sxbd"); err == nil {
		t.Error("expected gunzip error")
	}
}

func TestApplyToPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sxb")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ApplyToPath([]byte("NEWBINARY"), target); err != nil {
		t.Fatalf("ApplyToPath: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEWBINARY" {
		t.Errorf("after apply = %q, want NEWBINARY", got)
	}
}

func TestIsBrewManaged(t *testing.T) {
	cases := map[string]bool{
		"/opt/homebrew/bin/sxb":                      true, // under the Homebrew prefix
		"/opt/homebrew/Cellar/switchboard/1/bin/sxb": true,
		"/usr/local/Cellar/switchboard/1/bin/sxb":    true,
		"/home/linuxbrew/.linuxbrew/bin/sxb":         true,
		"/usr/local/bin/sxb":                         false,
		"/home/user/.local/bin/sxb":                  false,
	}
	for path, want := range cases {
		if got := IsBrewManaged(path); got != want {
			t.Errorf("IsBrewManaged(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestSemverNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.4.1", "v0.4.0", true},
		{"0.4.1", "v0.4.0", true},
		{"v1.0.0", "v0.9.9", true},
		{"v0.4.0", "v0.4.0", false},
		{"v0.1.0", "0.1.0-dev", false}, // release == dev of same version
		{"v0.4.0", "v0.4.1", false},
		{"v0.4.0-rc1", "v0.4.0", false}, // suffix ignored → equal
	}
	for _, c := range cases {
		if got := SemverNewer(c.a, c.b); got != c.want {
			t.Errorf("SemverNewer(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestFetchReleaseBadJSON(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"sxb": "x"})
	fakeGitHub(t, archive, "x  y\n")
	// Point at the bad-json handler directly.
	if _, err := fetchRelease(context.Background(), apiBase+"/bad-json"); err == nil {
		t.Error("expected decode error for non-JSON body")
	}
}
