package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	}
	for in, want := range cases {
		if got := shareableRemote(in); got != want {
			t.Errorf("shareableRemote(%q) = %q, want %q", in, got, want)
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

// A cut sends the release's commit, never whatever the checkout is at now.
func TestCodeSourceForReleaseSendsOnlyTheReleasesCommit(t *testing.T) {
	t.Parallel()
	checkout := codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitA}

	got := checkout.forRelease(release.Release{Git: release.Git{Commit: sourceCommitB}})
	if got.Commit != sourceCommitB || got.Subpath != "apps/web" || got.Remote != "github.com/acme/shop" {
		t.Errorf("recorded commit: %+v", got)
	}
	got = checkout.forRelease(release.Release{Git: release.Git{Commit: sourceCommitA}, Provenance: &release.Provenance{Commit: sourceCommitB}})
	if got.Commit != sourceCommitB {
		t.Errorf("provenance commit should win: %+v", got)
	}
	// A captured HEAD and a release whose commit is unknown: the source's
	// commit is unknown too, never the checkout's HEAD (false provenance
	// for a `release record` from a checkout at another commit).
	for name, r := range map[string]release.Release{
		"no commit":                 {},
		"invalid commit":            {Git: release.Git{Commit: "not-a-sha"}},
		"short commit":              {Git: release.Git{Commit: "abc1234"}},
		"invalid provenance commit": {Provenance: &release.Provenance{Commit: "zz"}},
	} {
		got := checkout.forRelease(r)
		if got.Commit != "" {
			t.Errorf("%s: commit = %q, want empty (never the checkout's HEAD)", name, got.Commit)
		}
		if got.Remote != "github.com/acme/shop" || got.Subpath != "apps/web" {
			t.Errorf("%s: lost the repository: %+v", name, got)
		}
	}
	// No checkout behind the ledger: the provenance's remote, still screened.
	got = codeSource{}.forRelease(release.Release{Provenance: &release.Provenance{Repo: "github.com/acme/shop", Commit: sourceCommitB}})
	if got.Remote != "github.com/acme/shop" || got.Commit != sourceCommitB {
		t.Errorf("provenance fallback: %+v", got)
	}
	if got = (codeSource{}).forRelease(release.Release{Provenance: &release.Provenance{Repo: "/home/sean/shop"}}); got.Remote != "" {
		t.Errorf("a path remote leaked: %+v", got)
	}
}

// Every cut sends the source: the legacy request and the snapshot one, where
// it rides beside the strictly decoded document rather than in it.
func TestCutSendsTheCodeSource(t *testing.T) {
	t.Parallel()
	src := codeSource{Remote: "github.com/acme/shop", Subpath: "apps/web", Commit: sourceCommitA, ReliantProjectID: "proj-1"}

	legacy := &snapshotCaller{}
	store := &hostedStore{client: legacy, project: "shop", source: fixedCodeSource(src)}
	rec := release.Release{Version: "v1", Project: "shop", RecordOnly: true, Git: release.Git{Commit: sourceCommitB}, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if _, err := store.Cut(context.Background(), rec); err != nil {
		t.Fatalf("legacy cut: %v", err)
	}
	want := map[string]any{"remote": "github.com/acme/shop", "subpath": "apps/web", "commit": sourceCommitB, "reliantProjectId": "proj-1"}
	assertCodeSource(t, "legacy cut", legacy.cut, want)

	snap := &snapshotCaller{capable: true}
	store = &hostedStore{client: snap, source: fixedCodeSource(src)}
	if _, err := store.Cut(context.Background(), snapshotCatalogRelease(t)); err != nil {
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

// A deploy that ships the checkout sends its HEAD; one that ships a named
// release sends the commit that release's cut recorded, never the checkout's
// HEAD; one whose release is not known when the attempt opens (--from), or
// cannot be read, sends the repository but no commit.
func TestDeployAttemptOpenSendsTheCodeSource(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		ships   bool
		version string
		commit  string
	}{
		"ships the checkout":            {ships: true, commit: sourceCommitA},
		"named release":                 {version: "v1", commit: sourceCommitB},
		"named release with no commit":  {version: "v0"},
		"named release never cut":       {version: "v9"},
		"release resolved later (from)": {},
	}
	for name, tc := range cases {
		c := &attemptCaller{releases: map[string]wireRelease{
			"v1": {Version: "v1", RecordOnly: true, GitCommit: sourceCommitB, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			"v0": {Version: "v0", RecordOnly: true, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		}}
		ctx, a := newTestAttempt("prod", "apply", io.Discard)
		a.shipsCheckout, a.release = tc.ships, tc.version
		l := envLedger{Bindings: &hostedStore{client: c, project: "shop", source: fixedCodeSource(codeSource{
			Remote: "github.com/acme/shop", Commit: sourceCommitA, ReliantProjectID: "proj-1",
		})}, Hosted: true}
		attachDeployAttempt(ctx, "prod", l)
		want := map[string]any{"remote": "github.com/acme/shop", "reliantProjectId": "proj-1"}
		if tc.commit != "" {
			want["commit"] = tc.commit
		}
		assertCodeSource(t, fmt.Sprintf("open (%s)", name), c.last(procOpenDeployAttempt), want)
		if got := c.count(procGetRelease); (got > 0) != (tc.version != "") {
			t.Errorf("%s: GetRelease called %d times", name, got)
		}
		a.finish(ctx, nil)
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
