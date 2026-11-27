// Copyright (c) 2026 Reliant Labs
package cliupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/reliant-labs/forge/pkg/selfupdate/sigstore"
	"github.com/stretchr/testify/require"
)

// The fixture is reliant v1.26.0's real signed set as release.yml published
// it (https://downloads.reliantlabs.io/cli/v1.26.0/): checksums.txt and
// reliant-release.sigstore.json, plus a snapshot of Sigstore's public-good
// trusted_root.json (the one forge's sigstore tests pin), so the test is
// offline. Production fetches the root through TUF.
func realRelease(t *testing.T, tag string) (selfupdate.Release, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for _, name := range []string{"checksums.txt", SignatureAsset} {
		b, err := os.ReadFile(filepath.Join("testdata", "v1.26.0", name))
		require.NoError(t, err)
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, b, 0o600))
		files[name] = p
	}
	return selfupdate.Release{
		Version: strings.TrimPrefix(tag, "v"),
		Tag:     tag,
		Assets:  []selfupdate.Asset{{Name: "checksums.txt"}},
		Bundle:  selfupdate.Asset{Name: SignatureAsset},
	}, files
}

func pinnedVerifier(t *testing.T) *sigstore.Verifier {
	t.Helper()
	root, err := os.ReadFile(filepath.Join("testdata", "trusted_root.json"))
	require.NoError(t, err)
	cfg := verifierConfig("")
	cfg.TrustedRoot = root
	v, err := sigstore.New(cfg)
	require.NoError(t, err)
	return v
}

// The verifier `reliant update` uses accepts a release release.yml really
// signed: the identity, issuer and predicate type here are the ones it signs.
func TestVerifiesARealReliantRelease(t *testing.T) {
	rel, files := realRelease(t, "v1.26.0")
	require.NoError(t, pinnedVerifier(t).Verify(context.Background(), rel, files))
}

// An older signed set offered under a newer tag is refused (the predicate's
// release must be the tag being installed), and so is a changed checksums.txt.
func TestRefusesAReplayedOrTamperedRelease(t *testing.T) {
	v := pinnedVerifier(t)

	rel, files := realRelease(t, "v1.27.0")
	require.Error(t, v.Verify(context.Background(), rel, files), "v1.26.0's signature must not install as v1.27.0")

	rel, files = realRelease(t, "v1.26.0")
	b, err := os.ReadFile(files["checksums.txt"])
	require.NoError(t, err)
	b[0] ^= 1
	require.NoError(t, os.WriteFile(files["checksums.txt"], b, 0o600))
	require.Error(t, v.Verify(context.Background(), rel, files), "a changed checksums.txt must not verify")
}

// The values pinned here are the ones the release doc publishes for every
// verifier; change them together.
func TestSigningContractMatchesTheReleaseDoc(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "dev-docs", "cli-release-artifacts.md"))
	require.NoError(t, err)
	for _, want := range []string{"`" + SignerIdentity + "`", "`" + SignerIssuer + "`", "`" + PredicateType + "`", "`" + SignatureAsset + "`", ManifestURL, `"repository": "` + Repository + `"`} {
		require.Contains(t, string(doc), want)
	}
}

func TestDefaultChannelFollowsTheBuildsTag(t *testing.T) {
	for v, want := range map[string]string{
		"v1.26.0":       ChannelLatest,
		"1.26.0":        ChannelLatest,
		"v1.26.0-rc.3":  ChannelAlpha,
		"1.27.0-beta.1": ChannelAlpha,
		"v2.0.0-alpha":  ChannelAlpha,
		// A pseudo-version (a `go install` between tags) is not a release
		// candidate.
		"v1.8.3-0.20261020032426-61cfe193a8e2": ChannelLatest,
		"":                                     ChannelLatest,
	} {
		require.Equal(t, want, DefaultChannel(v), v)
	}
}

// manifest is a schema-1 daemon-channels.json naming the given channels on
// base, each as {tag, commit_time_unix}.
func manifest(base string, channels map[string][2]any) string {
	var parts []string
	for name, ch := range channels {
		tag, ct := ch[0].(string), ch[1].(int)
		parts = append(parts, fmt.Sprintf(`%q: {
      "version": %q, "tag": %q, "commit": "c", "commit_time_unix": %d,
      "checksums": {"url": "%s/cli/%s/checksums.txt", "sha256": "00"},
      "bundle": {"url": "%s/cli/%s/reliant-release.sigstore.json"},
      "platforms": {"linux/amd64": {"url": "%s/cli/%s/reliant_linux_amd64.tar.gz", "sha256": "11"}}
    }`, name, strings.TrimPrefix(tag, "v"), tag, ct, base, tag, base, tag, base, tag))
	}
	return `{"schema": 1, "repository": "reliant-labs/reliant", "channels": {` + strings.Join(parts, ",") + `}}`
}

func serveManifest(t *testing.T, channels map[string][2]any) (string, *http.Client) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(manifest(srv.URL, channels)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/daemon-channels.json", srv.Client()
}

// An alpha client takes the newer of alpha and latest by build number, since
// a stable release after an RC train writes only latest.
func TestAlphaTakesTheNewerOfAlphaAndLatest(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		channels map[string][2]any
		channel  string
		want     string
	}{
		{"the stable release after the RCs", map[string][2]any{"alpha": {"v1.26.0-rc.3", 100}, "latest": {"v1.26.0", 200}}, ChannelAlpha, "v1.26.0"},
		{"a newer RC than the stable", map[string][2]any{"alpha": {"v1.27.0-rc.1", 300}, "latest": {"v1.26.0", 200}}, ChannelAlpha, "v1.27.0-rc.1"},
		{"no alpha channel yet", map[string][2]any{"latest": {"v1.26.0", 200}}, ChannelAlpha, "v1.26.0"},
		{"latest never takes alpha", map[string][2]any{"alpha": {"v1.27.0-rc.1", 300}, "latest": {"v1.26.0", 200}}, ChannelLatest, "v1.26.0"},
	} {
		t.Run(c.name, func(t *testing.T) {
			url, client := serveManifest(t, c.channels)
			rel, err := NewSource(url, client).Latest(ctx, c.channel)
			require.NoError(t, err)
			require.Equal(t, c.want, rel.Tag)
		})
	}

	url, client := serveManifest(t, map[string][2]any{"alpha": {"v1.27.0-rc.1", 300}})
	_, err := NewSource(url, client).Latest(ctx, ChannelLatest)
	require.ErrorContains(t, err, `no channel "latest"`)
}

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Config(Options{StateDir: dir, Current: selfupdate.Build{Version: "v1.26.0-rc.2"}, Verifier: fakeVerifier{}})
	require.NoError(t, err)
	require.Equal(t, Binary, cfg.Binary)
	require.Equal(t, ChannelAlpha, cfg.Channel, "an RC build follows alpha")
	require.Equal(t, dir, cfg.StateDir)
	require.Equal(t, DefaultConfirmWithin, cfg.ConfirmWithin)
	_, err = selfupdate.New(cfg)
	require.NoError(t, err)

	_, err = Config(Options{StateDir: dir, Current: selfupdate.Build{Version: "v1.26.0"}, Channel: "nightly"})
	require.ErrorContains(t, err, `unknown release channel "nightly"`)

	// The production config carries the Sigstore verifier, never none.
	cfg, err = Config(Options{StateDir: dir, Current: selfupdate.Build{Version: "v1.26.0"}})
	require.NoError(t, err)
	require.IsType(t, &sigstore.Verifier{}, cfg.Verifier)
	require.Equal(t, ChannelLatest, cfg.Channel)
}

// A build with neither a version nor a build number cannot be ordered against
// any release, so nothing is offered to it.
func TestConfigRefusesADevBuild(t *testing.T) {
	require.ErrorIs(t, orderable(selfupdate.Build{}), ErrDevBuild)
	require.NoError(t, orderable(selfupdate.Build{CommitTime: time.Unix(1, 0)}))
	require.NoError(t, orderable(selfupdate.Build{Version: "v1.26.0"}))
}

type fakeVerifier struct{}

func (fakeVerifier) Verify(context.Context, selfupdate.Release, map[string]string) error { return nil }

func TestAdviceNamesWhatUpdatesEachInstall(t *testing.T) {
	for _, c := range []struct {
		in   selfupdate.Install
		want string
	}{
		{selfupdate.Install{Kind: selfupdate.SelfOwned}, "reliant update"},
		{selfupdate.Install{Kind: selfupdate.Bundle}, "update the Reliant desktop app"},
		{selfupdate.Install{Kind: selfupdate.Stamped, Owner: OwnerDesktopApp}, "update the Reliant desktop app"},
		{selfupdate.Install{Kind: selfupdate.Stamped, Owner: OwnerWorkspaceImage}, "the Reliant platform updates this machine"},
		{selfupdate.Install{Kind: selfupdate.Managed, Owner: OwnerPlatform}, "the Reliant platform updates this machine"},
		{selfupdate.Install{Kind: selfupdate.PackageManager, Owner: "homebrew", Hint: "brew upgrade reliant"}, "brew upgrade reliant"},
		{selfupdate.Install{Kind: selfupdate.PackageManager, Owner: "nix"}, "update it with nix"},
		{selfupdate.Install{Kind: selfupdate.ReadOnly}, "sudo reliant update"},
	} {
		require.Contains(t, Advice(c.in), c.want, c.in.Kind.String())
	}
}

// Every place the desktop app puts its reliant is refused by `reliant
// update`: inside the bundle by the path, and the copies it makes outside it
// (Windows) by the managed-by file it writes beside them.
func TestDetectRefusesTheDesktopAppsInstalls(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) string {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o755))
		return p
	}
	mac := write("Applications/Reliant.app/Contents/Resources/server/mac-arm64/reliant-backend", "")
	deb := write("opt/Reliant/resources/server/linux-amd64/reliant-backend", "")
	winCopy := write("AppData/Local/Reliant/bin/reliant.exe", "")
	write("AppData/Local/Reliant/bin/reliant.exe.managed-by", OwnerDesktopApp+"\n")
	nsisCopy := write("Programs/Reliant/cli/reliant.exe", "")
	write("Programs/Reliant/cli/reliant.exe.managed-by", OwnerDesktopApp+"\r\n")
	own := write("home/.local/bin/reliant", "")
	link := filepath.Join(root, "usr-local-bin-reliant")
	require.NoError(t, os.Symlink(mac, link))

	// The stamps and the managed-host predicate come before selfupdate's
	// container probe, so these hold on any host.
	for _, path := range []string{winCopy, nsisCopy} {
		in, err := selfupdate.Detect(selfupdate.DetectOptions{Path: path})
		require.NoError(t, err)
		require.Equal(t, selfupdate.Stamped, in.Kind, "%s: %s", path, in.Reason)
		require.Equal(t, OwnerDesktopApp, in.Owner)
	}
	in, err := selfupdate.Detect(selfupdate.DetectOptions{Path: own, Managed: ManagedHost(true)})
	require.NoError(t, err)
	require.Equal(t, selfupdate.Managed, in.Kind, "a cloud machine's binary is the platform's wherever it is")

	// The path rules run only after it.
	plainHost(t)
	for path, want := range map[string]selfupdate.Kind{
		mac: selfupdate.Bundle, deb: selfupdate.Bundle, link: selfupdate.Bundle, own: selfupdate.SelfOwned,
	} {
		in, err := selfupdate.Detect(selfupdate.DetectOptions{Path: path})
		require.NoError(t, err)
		require.Equal(t, want, in.Kind, "%s: %s", path, in.Reason)
	}
}

// The marker the desktop app writes names the owner Advice recognises.
func TestDesktopCopiesNameTheirOwner(t *testing.T) {
	js, err := os.ReadFile(filepath.Join("..", "..", "electron", "src", "cli-installer.js"))
	require.NoError(t, err)
	require.Contains(t, string(js), `const MANAGED_BY_OWNER = "`+OwnerDesktopApp+`";`)
	require.Contains(t, string(js), "writeManagedByMarker(target);")

	nsis, err := os.ReadFile(filepath.Join("..", "..", "electron", "build", "installer.nsh"))
	require.NoError(t, err)
	require.Contains(t, string(nsis), `FileOpen $1 "$INSTDIR\cli\reliant.exe.managed-by" w`)
	require.Contains(t, string(nsis), `FileWrite $1 "`+OwnerDesktopApp)
}

// The workspace image's binary is stamped as the platform's. The release's
// backend binaries are not: they are both the desktop app's (refused by
// path) and the standalone CLI archives (self-owned), the same bytes.
func TestWorkspaceImageIsStampedAndTheCLIArchivesAreNot(t *testing.T) {
	stamp := "-X github.com/reliant-labs/forge/pkg/selfupdate.managedBy=" + OwnerWorkspaceImage
	for _, f := range []string{"Dockerfile", "Dockerfile.debug"} {
		b, err := os.ReadFile(filepath.Join("..", "..", f))
		require.NoError(t, err)
		require.Contains(t, string(b), stamp, f)
	}
	release, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	require.NoError(t, err)
	require.NotContains(t, string(release), "selfupdate.managedBy")
}

// releaseNewer orders by build number, and by semver only without one.
func TestReleaseNewer(t *testing.T) {
	at := func(v string, unix int64) selfupdate.Release {
		r := selfupdate.Release{Version: v}
		if unix > 0 {
			r.CommitTime = time.Unix(unix, 0)
		}
		return r
	}
	require.True(t, releaseNewer(at("1.26.0", 200), at("1.27.0-rc.1", 100)))
	require.False(t, releaseNewer(at("1.26.0", 0), at("1.27.0-rc.1", 0)))
	require.True(t, releaseNewer(at("1.27.0", 0), at("1.27.0-rc.1", 0)))
}
