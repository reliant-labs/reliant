package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

var (
	sourceCommitA = strings.Repeat("a", 40)
	sourceCommitB = strings.Repeat("b", 40)
)

// fixedCodeSource is a probe that captures src without running git.
func fixedCodeSource(src codeSource) *codeSourceProbe {
	return &codeSourceProbe{capture: func(context.Context, string, func(string) string) codeSource { return src }}
}

func TestShareableRemote(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://github.com/Acme/Shop.git":                       "github.com/Acme/Shop",
		"git@GitHub.com:acme/shop.git":                           "github.com/acme/shop",
		"ssh://git@gitlab.example.com:2222/group/sub/shop.git":   "gitlab.example.com/group/sub/shop",
		"https://x-access-token:ghp_secret@github.com/acme/shop": "github.com/acme/shop",
		"https://oauth2:glpat-secret@gitlab.com/acme/shop.git/":  "gitlab.com/acme/shop",
		"ssh://git@github.com/acme/shop.git":                     "github.com/acme/shop",
		"git://git.example.org/acme/shop":                        "git.example.org/acme/shop",
		// The SCP form without a user: CanonicalRepo alone reads
		// "github.com:acme" as a host and a port.
		"github.com:acme/shop.git": "github.com/acme/shop",
		// Nothing that names this machine, and nothing still carrying a
		// credential or a query, is shareable.
		"/home/sean/src/shop":                    "",
		"file:///home/sean/src/shop":             "",
		"../shop":                                "",
		`C:\src\shop`:                            "",
		"C:/src/shop":                            "",
		"https://github.com/acme/shop?token=abc": "",
		"github.com":                             "",
		"":                                       "",
		// Local remotes that canonicalization would make look hosted:
		// classified as local from the ORIGINAL url, never transmitted.
		"sibling/repo":                        "",
		"github.com/acme/shop":                "",
		"./sibling/repo":                      "",
		"file://localhost/home/sean/src/shop": "",
		"file://github.com/acme/shop":         "",
		"FILE:///home/sean/src/shop":          "",
		"/srv/git/github.com/acme/shop.git":   "",
		"~/src/shop":                          "",
		// A remote helper, an unknown scheme, a URL with no host.
		"persistent-https::github.com/acme/shop": "",
		"rsync://github.com/acme/shop":           "",
		"https:///acme/shop":                     "",
		// A host that names this machine, or resolves only through its
		// own configuration (an ssh alias, a search domain).
		"ssh://localhost/acme/shop":   "",
		"git@localhost:acme/shop":     "",
		"https://127.0.0.1/acme/shop": "",
		"gitserver:acme/shop":         "",
		"git@gh:acme/shop":            "",
		"github.com:":                 "",
	}
	for in, want := range cases {
		if got := shareableRemote(in); got != want {
			t.Errorf("shareableRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

// A remote that is ALREADY canonical — a release's provenance holds one —
// has lost the url that said whether it was a network remote, so it is
// screened as one: a fully qualified host, never this machine.
func TestShareableCanonical(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"github.com/acme/shop":          "github.com/acme/shop",
		"gitlab.example.com/g/sub/shop": "gitlab.example.com/g/sub/shop",
		"10.0.0.5/acme/shop":            "10.0.0.5/acme/shop",
		// What a relative local remote, or a stripped file:// url,
		// canonicalizes to.
		"sibling/repo":           "",
		"/home/sean/src/shop":    "",
		"localhost/acme/shop":    "",
		"127.0.0.1/acme/shop":    "",
		"github.com":             "",
		"github.com/":            "",
		"user@github.com/a/b":    "",
		"github.com/acme/shop?x": "",
	}
	for in, want := range cases {
		if got := shareableCanonical(in); got != want {
			t.Errorf("shareableCanonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReliantProjectIDIsAnOpaqueID(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"6f1c2b9e-0d3a-4f8e-9b7c-2a1d0e5f4c3b": "6f1c2b9e-0d3a-4f8e-9b7c-2a1d0e5f4c3b",
		"  proj_123  ":                         "proj_123",
		"":                                     "",
		"has space":                            "",
		"line\nbreak":                          "",
		"-leading-dash":                        "",
		strings.Repeat("x", 129):               "",
		"proj/../../etc":                       "",
	} {
		if got := reliantProjectID(in); got != want {
			t.Errorf("reliantProjectID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodeSourceWireOmitsWhatIsUnknown(t *testing.T) {
	t.Parallel()
	if w := (codeSource{}).wire(); w != nil {
		t.Errorf("an empty source sends %v, want nothing", w)
	}
	w := codeSource{Remote: "github.com/acme/shop", ReliantProjectID: "p1"}.wire()
	if len(w) != 2 || w["remote"] != "github.com/acme/shop" || w["reliantProjectId"] != "p1" {
		t.Errorf("wire = %v", w)
	}
}

// The source of a release — cut, recorded or deployed — is the release's own
// tuple: its recorded repository and commit, never the checkout's HEAD, and
// the checkout's directory and chat only when the release's provenance names
// a shareable repository that is the checkout's own. A release whose
// provenance does not establish that sends its commit alone (or nothing), so
// a release recorded elsewhere is never attributed to this checkout.
func TestCodeSourceForReleaseIsTheReleasesTuple(t *testing.T) {
	t.Parallel()
	clone := codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitA, ReliantProjectID: "proj-1"}
	elsewhere := codeSource{Remote: "github.com/acme/ops", Subpath: "deploy", Commit: sourceCommitA, ReliantProjectID: "proj-1"}
	recorded := func(repo, commit string) release.Release {
		return release.Release{Git: release.Git{Commit: sourceCommitA}, Provenance: &release.Provenance{Repo: repo, Commit: commit}}
	}
	cases := map[string]struct {
		checkout codeSource
		r        release.Release
		want     codeSource
	}{
		"a clone of the release's repository at another HEAD": {
			checkout: clone, r: recorded("github.com/acme/shop", sourceCommitB),
			want: codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitB, ReliantProjectID: "proj-1"},
		},
		"the release's repository, recorded as a url": {
			checkout: clone, r: recorded("https://github.com/acme/shop.git", sourceCommitB),
			want: codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitB, ReliantProjectID: "proj-1"},
		},
		"a checkout of another repository": {
			checkout: elsewhere, r: recorded("github.com/acme/shop", sourceCommitB),
			want: codeSource{Remote: "github.com/acme/shop", Commit: sourceCommitB},
		},
		"a fork's release from the upstream's checkout": {
			checkout: clone, r: recorded("github.com/acme/fork", sourceCommitB),
			want: codeSource{Remote: "github.com/acme/fork", Commit: sourceCommitB},
		},
		"no checkout": {
			r:    recorded("github.com/acme/shop", sourceCommitB),
			want: codeSource{Remote: "github.com/acme/shop", Commit: sourceCommitB},
		},
		// Provenance that does not establish the release's repository:
		// the commit alone, whatever the checkout is.
		"no provenance (release record)": {
			checkout: clone, r: release.Release{Git: release.Git{Commit: sourceCommitB}},
			want: codeSource{Commit: sourceCommitB},
		},
		"provenance with an empty repository": {
			checkout: clone, r: recorded("", sourceCommitB),
			want: codeSource{Commit: sourceCommitB},
		},
		"provenance with an invalid repository": {
			checkout: clone, r: recorded("github.com/acme/shop?token=abc", sourceCommitB),
			want: codeSource{Commit: sourceCommitB},
		},
		"provenance with an absolute local path": {
			checkout: clone, r: recorded("/home/sean/src/shop", sourceCommitB),
			want: codeSource{Commit: sourceCommitB},
		},
		"provenance with a canonicalized relative local remote": {
			checkout: clone, r: recorded("sibling/repo", sourceCommitB),
			want: codeSource{Commit: sourceCommitB},
		},
		"provenance with a file url": {
			checkout: clone, r: recorded("file:///home/sean/src/shop", sourceCommitB),
			want: codeSource{Commit: sourceCommitB},
		},
		// Nothing known: nothing sent, and never the checkout's HEAD.
		"no provenance and no commit": {
			checkout: clone, r: release.Release{},
			want: codeSource{},
		},
		"the release's repository but an invalid commit": {
			checkout: clone, r: release.Release{Git: release.Git{Commit: "abc1234"}, Provenance: &release.Provenance{Repo: "github.com/acme/shop", Commit: "zz"}},
			want: codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", ReliantProjectID: "proj-1"},
		},
	}
	for name, tc := range cases {
		if got := tc.checkout.forRelease(tc.r); got != tc.want {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
	// The provenance commit wins over the legacy git record.
	if got := (codeSource{}).forRelease(release.Release{Git: release.Git{Commit: sourceCommitA}, Provenance: &release.Provenance{Commit: sourceCommitB}}); got.Commit != sourceCommitB {
		t.Errorf("provenance commit should win: %+v", got)
	}
	for name, r := range map[string]release.Release{
		"invalid commit": {Git: release.Git{Commit: "not-a-sha"}},
		"short commit":   {Git: release.Git{Commit: "abc1234"}},
	} {
		if got := clone.forRelease(r); got != (codeSource{}) {
			t.Errorf("%s: %+v, want nothing (never the checkout's HEAD or repository)", name, got)
		}
	}
}

// Every cut sends the source: the legacy request and the snapshot one, where
// it rides beside the strictly decoded document rather than in it.
func TestCutSendsTheCodeSource(t *testing.T) {
	t.Parallel()
	src := codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitA, ReliantProjectID: "proj-1"}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// A cut whose provenance names the checkout's repository: the whole
	// tuple, with the release's commit.
	legacy := &snapshotCaller{}
	store := &hostedStore{client: legacy, project: "shop", source: fixedCodeSource(src)}
	cut := release.Release{Version: "v1", Project: "shop", RecordOnly: true, Git: release.Git{Commit: sourceCommitB}, CreatedAt: at,
		Provenance: &release.Provenance{Repo: "github.com/acme/shop", Commit: sourceCommitB}}
	if _, err := store.Cut(context.Background(), cut); err != nil {
		t.Fatalf("legacy cut: %v", err)
	}
	want := map[string]any{"remote": "github.com/acme/shop", "subpath": "apps/web", "commit": sourceCommitB, "reliantProjectId": "proj-1"}
	assertCodeSource(t, "legacy cut", legacy.cut, want)

	// A recorded release (no provenance): nothing says this checkout's
	// repository built it, so its commit alone.
	recorded := &snapshotCaller{}
	store = &hostedStore{client: recorded, project: "shop", source: fixedCodeSource(src)}
	if _, err := store.Cut(context.Background(), release.Release{Version: "v2", Project: "shop", RecordOnly: true, Git: release.Git{Commit: sourceCommitB}, CreatedAt: at}); err != nil {
		t.Fatalf("record: %v", err)
	}
	assertCodeSource(t, "record", recorded.cut, map[string]any{"commit": sourceCommitB})

	snap := &snapshotCaller{capable: true}
	store = &hostedStore{client: snap, source: fixedCodeSource(src)}
	catalog := snapshotCatalogRelease(t)
	catalog.Provenance = &release.Provenance{Repo: "github.com/acme/shop"}
	if _, err := store.Cut(context.Background(), catalog); err != nil {
		t.Fatalf("snapshot cut: %v", err)
	}
	if snap.cut["snapshotRelease"] == nil {
		t.Fatalf("not a snapshot cut: %v", snap.cut)
	}
	if doc, _ := snap.cut["snapshotRelease"].(map[string]any); doc["codeSource"] != nil {
		t.Errorf("the source leaked into the strictly decoded document")
	}
	// The catalog release names no commit of its own, so none is sent —
	// not the checkout's HEAD.
	delete(want, "commit")
	assertCodeSource(t, "snapshot cut", snap.cut, want)
}

// A ledger with no checkout behind it and a release with no provenance sends
// no codeSource key at all: the request is the one it always was.
func TestCutWithoutASourceSendsNone(t *testing.T) {
	t.Parallel()
	fake := &snapshotCaller{}
	store := &hostedStore{client: fake, project: "shop"}
	rec := release.Release{Version: "v1", Project: "shop", RecordOnly: true, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := store.Cut(context.Background(), rec); err != nil {
		t.Fatalf("cut: %v", err)
	}
	if _, ok := fake.cut["codeSource"]; ok {
		t.Errorf("sent codeSource %v with nothing known", fake.cut["codeSource"])
	}
}

// attemptReleases are the releases the attempt tests' ledger holds: v1 cut
// from github.com/acme/shop at commit B, v2 recorded with commit B and no
// repository, v0 recording neither.
func attemptReleases() map[string]wireRelease {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return map[string]wireRelease{
		"v1": {Version: "v1", RecordOnly: true, GitCommit: sourceCommitB, CreatedAt: at,
			Provenance: &wireProvenance{Repo: "github.com/acme/shop", Commit: sourceCommitB}},
		"v2": {Version: "v2", RecordOnly: true, GitCommit: sourceCommitB, CreatedAt: at},
		"v0": {Version: "v0", RecordOnly: true, CreatedAt: at},
	}
}

// attemptLedger is a hosted ledger over c whose checkout captured src.
func attemptLedger(c cloudCaller, src codeSource) envLedger {
	return envLedger{Bindings: &hostedStore{client: c, project: "shop", source: fixedCodeSource(src)}, Hosted: true}
}

// sourcesSent is every codeSource the attempt sent with procedure.
func (c *attemptCaller) sourcesSent(procedure string) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for i, p := range c.calls {
		if src, ok := c.bodies[i]["codeSource"].(map[string]any); ok && p == procedure {
			out = append(out, src)
		}
	}
	return out
}

// A deploy that ships the checkout sends its source, HEAD included. One that
// ships a named release sends THAT release's repository and commit, with the
// checkout's directory and chat only when the checkout is a clone of the same
// repository. One whose release is not known when the attempt opens (--from),
// or cannot be read, sends nothing at the open.
func TestDeployAttemptOpenSendsTheCodeSource(t *testing.T) {
	t.Parallel()
	clone := codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitA, ReliantProjectID: "proj-1"}
	elsewhere := codeSource{Remote: "github.com/acme/ops", Subpath: "deploy", Commit: sourceCommitA, ReliantProjectID: "proj-1"}
	cases := map[string]struct {
		checkout codeSource
		ships    bool
		version  string
		want     map[string]any // nil: no codeSource key
	}{
		"ships the checkout": {checkout: clone, ships: true,
			want: map[string]any{"remote": "github.com/acme/shop", "subpath": "apps/web", "commit": sourceCommitA, "reliantProjectId": "proj-1"}},
		"named release from a clone": {checkout: clone, version: "v1",
			want: map[string]any{"remote": "github.com/acme/shop", "subpath": "apps/web", "commit": sourceCommitB, "reliantProjectId": "proj-1"}},
		"named release from another repository's checkout": {checkout: elsewhere, version: "v1",
			want: map[string]any{"remote": "github.com/acme/shop", "commit": sourceCommitB}},
		"named release with no recorded repository": {checkout: clone, version: "v2",
			want: map[string]any{"commit": sourceCommitB}},
		"named release recording nothing": {checkout: clone, version: "v0"},
		"named release never cut":         {checkout: clone, version: "v9"},
		"release resolved later (from)":   {checkout: elsewhere},
	}
	for name, tc := range cases {
		c := &attemptCaller{releases: attemptReleases()}
		ctx, a := newTestAttempt("prod", "apply", io.Discard)
		a.shipsCheckout, a.release = tc.ships, tc.version
		attachDeployAttempt(ctx, "prod", attemptLedger(c, tc.checkout))
		open := c.last(procOpenDeployAttempt)
		if tc.want == nil {
			if src, ok := open["codeSource"]; ok {
				t.Errorf("open (%s): sent codeSource %v, want none", name, src)
			}
		} else {
			assertCodeSource(t, fmt.Sprintf("open (%s)", name), open, tc.want)
		}
		if got := c.count(procGetRelease); (got > 0) != (tc.version != "") {
			t.Errorf("%s: GetRelease called %d times", name, got)
		}
		a.finish(ctx, nil)
		// Sent once, at the open: no report repeats it, and a release that
		// could not be read is not re-read on every heartbeat.
		if got := c.sourcesSent(procReportDeployAttempt); len(got) != 0 {
			t.Errorf("%s: reports re-sent the source: %v", name, got)
		}
		if got := c.count(procGetRelease); got > 1 {
			t.Errorf("%s: GetRelease called %d times, want at most once", name, got)
		}
	}
}

// A --from deploy learns its release only after the attempt opened: the open
// sends no source, and the report after the release is noted sends that
// release's tuple, once — whichever checkout runs the deploy.
func TestDeployAttemptFromSendsTheShippedSourceOnAReport(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		checkout codeSource
		want     map[string]any
	}{
		"from another repository's checkout": {
			checkout: codeSource{Remote: "github.com/acme/ops", Subpath: "deploy", Commit: sourceCommitA, ReliantProjectID: "proj-1"},
			want:     map[string]any{"remote": "github.com/acme/shop", "commit": sourceCommitB},
		},
		"from a clone at another HEAD": {
			checkout: codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitA, ReliantProjectID: "proj-1"},
			want:     map[string]any{"remote": "github.com/acme/shop", "subpath": "apps/web", "commit": sourceCommitB, "reliantProjectId": "proj-1"},
		},
	}
	for name, tc := range cases {
		c := &attemptCaller{releases: attemptReleases()}
		ctx, a := newTestAttempt("prod", "apply", io.Discard)
		attachDeployAttempt(ctx, "prod", attemptLedger(c, tc.checkout))
		if src, ok := c.last(procOpenDeployAttempt)["codeSource"]; ok {
			t.Fatalf("%s: the open sent %v before the release was known", name, src)
		}
		noteDeployPhase(ctx, "resolve")
		noteDeployRelease(ctx, "v1")
		noteDeployPhase(ctx, "promote")
		// finish delivers a source no heartbeat carried yet, so this is
		// exact whatever the heartbeat timing.
		a.finish(ctx, nil)
		sent := c.sourcesSent(procReportDeployAttempt)
		if len(sent) != 1 {
			t.Fatalf("%s: reports carried %d sources, want exactly 1: %v", name, len(sent), sent)
		}
		assertCodeSource(t, fmt.Sprintf("report (%s)", name), map[string]any{"codeSource": sent[0]}, tc.want)
		if c.sourcesSent(procCloseDeployAttempt) != nil {
			t.Errorf("%s: the close carried a source", name)
		}
		if got := c.count(procGetRelease); got != 1 {
			t.Errorf("%s: GetRelease called %d times, want 1", name, got)
		}
	}
}

// A --from release that cannot be read is read once, and nothing is sent: a
// checkout's repository beside an unknown release is no source.
func TestDeployAttemptFromUnreadableReleaseSendsNothing(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{releases: attemptReleases()}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	attachDeployAttempt(ctx, "prod", attemptLedger(c, codeSource{Remote: "github.com/acme/shop", Commit: sourceCommitA}))
	noteDeployRelease(ctx, "v9")
	noteDeployPhase(ctx, "promote")
	a.finish(ctx, nil)
	for _, p := range []string{procOpenDeployAttempt, procReportDeployAttempt, procCloseDeployAttempt} {
		if got := c.sourcesSent(p); got != nil {
			t.Errorf("%s sent %v", p, got)
		}
	}
	if got := c.count(procGetRelease); got != 1 {
		t.Errorf("GetRelease called %d times, want 1", got)
	}
}

func TestDeployAttemptShipsCheckoutOnlyWithoutANamedRelease(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		f    deployCmdFlags
		want bool
	}{
		"no release":     {deployCmdFlags{}, true},
		"named release":  {deployCmdFlags{promote: promoteCmdFlags{version: "v1"}}, false},
		"from other env": {deployCmdFlags{promote: promoteCmdFlags{fromEnv: "staging"}}, false},
		"from promotion": {deployCmdFlags{promote: promoteCmdFlags{fromPromotionID: "prom-1"}}, false},
	}
	for name, tc := range cases {
		if got := deployShipsCheckout(tc.f); got != tc.want {
			t.Errorf("%s: shipsCheckout = %v, want %v", name, got, tc.want)
		}
	}
}

func TestAttachCodeSourceOnlyToAHostedLedger(t *testing.T) {
	t.Parallel()
	hs := &hostedStore{}
	attachCodeSource(envLedger{Bindings: hs, Releases: hs, Hosted: true}, "/checkout", nil)
	if hs.source == nil || hs.source.dir != "/checkout" {
		t.Errorf("hosted ledger: source = %+v", hs.source)
	}
	empty := &hostedStore{}
	attachCodeSource(envLedger{Bindings: empty, Hosted: true}, "", nil)
	if empty.source != nil {
		t.Errorf("no checkout should attach no source")
	}
	// A file ledger is unchanged (and must not panic).
	_ = attachCodeSource(envLedger{}, "/checkout", nil)
}

// The capture against real checkouts: a nested forge.yaml in a repository
// with a credentialed remote, a repository with no commit and no remote, and
// a directory outside any checkout.
func TestCaptureCodeSource(t *testing.T) {
	testkit.Slow(t, "runs git")
	t.Parallel()
	env := func(k string) string { return map[string]string{reliantProjectIDEnv: "proj-1"}[k] }

	repo := t.TempDir()
	app := filepath.Join(repo, "apps", "web")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, app, "forge.yaml", "name: web\n")
	mustGit(t, repo, "init", "-q")
	mustGit(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "add", "-A")
	mustGit(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "init")
	mustGit(t, repo, "remote", "add", "origin", "https://x-access-token:secret@GitHub.com/Acme/Shop.git")
	// A dirty tree still sends its HEAD.
	writeFixtureFile(t, app, "dirty.txt", "uncommitted\n")
	head := strings.TrimSpace(gitOutput(t, repo, "rev-parse", "HEAD"))

	got := captureCodeSource(context.Background(), app, env)
	want := codeSource{Remote: "github.com/Acme/Shop", Subpath: "apps/web", Commit: head, ReliantProjectID: "proj-1"}
	if got != want {
		t.Errorf("nested app: %+v, want %+v", got, want)
	}
	if got := captureCodeSource(context.Background(), repo, nil); got.Subpath != "" || got.Commit != head {
		t.Errorf("repo root: %+v", got)
	}

	unborn := t.TempDir()
	mustGit(t, unborn, "init", "-q")
	if got := captureCodeSource(context.Background(), unborn, env); got != (codeSource{ReliantProjectID: "proj-1"}) {
		t.Errorf("no commit, no remote: %+v", got)
	}

	if got := captureCodeSource(context.Background(), t.TempDir(), env); got != (codeSource{ReliantProjectID: "proj-1"}) {
		t.Errorf("not a checkout: %+v", got)
	}
}

func assertCodeSource(t *testing.T, what string, body map[string]any, want map[string]any) {
	t.Helper()
	got, _ := body["codeSource"].(map[string]any)
	if len(got) != len(want) {
		t.Errorf("%s: codeSource = %v, want %v", what, got, want)
		return
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: codeSource.%s = %v, want %v", what, k, got[k], v)
		}
	}
}

// noteReleaseQuietly names the attempt's release without nudging a
// heartbeat, so only the calls a test makes report.
func noteReleaseQuietly(a *deployAttempt, version string) {
	a.mu.Lock()
	a.release = version
	a.mu.Unlock()
}

// reportsCarryingSource counts the reports that carried a code source.
func reportsCarryingSource(c *attemptCaller) int { return len(c.sourcesSent(procReportDeployAttempt)) }

// A source still owed at finish survives a transient failure of the final
// report: it is retried, with backoff, until the server takes it — and the
// release behind it is read once, not once per try.
func TestDeployAttemptFinalFlushRetriesATransientFailure(t *testing.T) {
	t.Parallel()
	unavailable := &cloud.Error{Code: cloud.CodeUnavailable, Message: "try again"}
	c := &attemptCaller{releases: attemptReleases(), reportErrs: []error{unavailable, unavailable}}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	a.flushBackoff = time.Millisecond
	attachDeployAttempt(ctx, "prod", attemptLedger(c, codeSource{Remote: "github.com/acme/ops", Commit: sourceCommitA}))
	noteReleaseQuietly(a, "v1")

	a.finish(ctx, nil)
	if got := c.count(procReportDeployAttempt); got != 3 {
		t.Fatalf("final reports = %d, want 3 (two transient failures, then delivery)", got)
	}
	if got := reportsCarryingSource(c); got != 3 {
		t.Errorf("reports carrying the source = %d, want every try to carry it", got)
	}
	assertCodeSource(t, "delivered", map[string]any{"codeSource": c.sourcesSent(procReportDeployAttempt)[2]},
		map[string]any{"remote": "github.com/acme/shop", "commit": sourceCommitB})
	if got := c.count(procGetRelease); got != 1 {
		t.Errorf("GetRelease called %d times, want 1", got)
	}
	a.mu.Lock()
	done, pending := a.codeSourceDone, a.codeSourcePending
	a.mu.Unlock()
	if !done || pending != nil {
		t.Errorf("after delivery: done = %v, pending = %v", done, pending)
	}
	if c.last(procCloseDeployAttempt) == nil {
		t.Error("the attempt was never closed")
	}
}

// The final flush is bounded: a server that keeps failing is tried
// attemptFlushTries times, the source stays undelivered (never cleared as
// sent), and the attempt still closes.
func TestDeployAttemptFinalFlushIsBounded(t *testing.T) {
	t.Parallel()
	var errs []error
	for range attemptFlushTries + 2 {
		errs = append(errs, &cloud.Error{Code: cloud.CodeUnavailable, Message: "down"})
	}
	c := &attemptCaller{releases: attemptReleases(), reportErrs: errs}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	a.flushBackoff = time.Millisecond
	attachDeployAttempt(ctx, "prod", attemptLedger(c, codeSource{}))
	noteReleaseQuietly(a, "v1")

	a.finish(ctx, nil)
	if got := c.count(procReportDeployAttempt); got != attemptFlushTries {
		t.Errorf("final reports = %d, want %d", got, attemptFlushTries)
	}
	a.mu.Lock()
	done, pending := a.codeSourceDone, a.codeSourcePending
	a.mu.Unlock()
	if done || pending == nil {
		t.Errorf("an undelivered source was cleared: done = %v, pending = %v", done, pending)
	}
	if c.last(procCloseDeployAttempt) == nil {
		t.Error("the attempt was never closed")
	}
}

// Reports race — the heartbeat, a nudge, finish — and the source is
// resolved and sent by exactly one of them: report1 is serialized, so the
// first resolves and delivers it and the rest find nothing owed.
func TestDeployAttemptConcurrentReportsSendTheSourceOnce(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{releases: attemptReleases()}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	attachDeployAttempt(ctx, "prod", attemptLedger(c, codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web"}))
	noteReleaseQuietly(a, "v1")

	const reporters = 16
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(reporters)
	done.Add(reporters)
	for range reporters {
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			_ = a.report1(context.Background())
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	a.finish(ctx, nil)

	if got := reportsCarryingSource(c); got != 1 {
		t.Errorf("reports carrying the source = %d, want exactly 1", got)
	}
	if got := c.count(procGetRelease); got != 1 {
		t.Errorf("GetRelease called %d times, want 1", got)
	}
}
