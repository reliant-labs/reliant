// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gitpublish"
)

const publishToken = "ghs_ONE_SHOT_publish_token_0123456789"

func needGit(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: spawns git against temp repositories; runs in the full lane")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// pubGit runs a git command in dir with a clean, hermetic environment.
func pubGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// publishFixture is a bare remote and a clone of it with one commit on main.
type publishFixture struct {
	remote, clone string
}

func newPublishFixture(t *testing.T) publishFixture {
	t.Helper()
	root := t.TempDir()
	f := publishFixture{remote: filepath.Join(root, "remote.git"), clone: filepath.Join(root, "clone")}
	pubGit(t, root, "init", "-q", "--bare", "-b", "main", f.remote)
	pubGit(t, root, "clone", "-q", f.remote, f.clone)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "README.md"), []byte("hello\n"), 0o644))
	pubGit(t, f.clone, "add", "-A")
	pubGit(t, f.clone, "commit", "-q", "-m", "init")
	pubGit(t, f.clone, "push", "-q", "origin", "main")
	return f
}

// allowFileRemotes lets a publish push to the local bare repository.
func allowFileRemotes(t *testing.T) {
	t.Helper()
	old := publishURLAllowed
	publishURLAllowed = func(u *url.URL) bool { return u.Scheme == "file" || u.Scheme == "http" }
	t.Cleanup(func() { publishURLAllowed = old })
}

func prepare(t *testing.T, worktree, message string) gitpublish.PrepareResponse {
	t.Helper()
	payload, _ := json.Marshal(gitpublish.PrepareRequest{Worktree: worktree, Message: message,
		Trailers: []gitpublish.Trailer{{Key: "Reliant-Run", Value: "run-42"}, {Key: "Reliant-Workflow", Value: "autopilot-issue"}}})
	raw, err := handleGitPublishPrepare(context.Background(), payload)
	require.NoError(t, err)
	var resp gitpublish.PrepareResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

func push(t *testing.T, req gitpublish.PushRequest) gitpublish.PushResponse {
	t.Helper()
	payload, _ := json.Marshal(req)
	raw, err := handleGitPublish(context.Background(), payload)
	require.NoError(t, err)
	var resp gitpublish.PushResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	return resp
}

func (f publishFixture) pushReq(branch, sha string) gitpublish.PushRequest {
	return gitpublish.PushRequest{Worktree: f.clone, RemoteURL: "file://" + f.remote, Branch: branch, ExpectSHA: sha, Token: publishToken}
}

func remoteHead(t *testing.T, f publishFixture, branch string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = f.remote
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func TestGitPublish_PushesAFeatureBranchWithTrailers(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "fix.go"), []byte("package fix\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "README.md"), []byte("hello\nworld\n"), 0o644))

	prep := prepare(t, f.clone, "Fix the save bug")
	require.True(t, prep.Committed)
	assert.Equal(t, 1, prep.Commits)
	assert.Equal(t, 2, prep.FilesChanged)
	assert.Equal(t, 2, prep.Insertions)
	assert.Equal(t, 0, prep.Deletions)
	assert.Equal(t, pubGit(t, f.clone, "rev-parse", "HEAD~1"), prep.BaseSHA)

	res := push(t, f.pushReq("autopilot/issue-1", prep.HeadSHA))
	require.True(t, res.Pushed, res.Refusal)
	assert.Equal(t, prep.HeadSHA, remoteHead(t, f, "autopilot/issue-1"), "the pushed sha is the prepared sha")

	msg := pubGit(t, f.remote, "log", "-1", "--format=%B", "autopilot/issue-1")
	assert.Contains(t, msg, "Fix the save bug")
	assert.Contains(t, msg, "Reliant-Run: run-42")
	assert.Contains(t, msg, "Reliant-Workflow: autopilot-issue")
	paragraphs := strings.Split(strings.TrimSpace(msg), "\n\n")
	assert.Contains(t, paragraphs[len(paragraphs)-1], "Reliant-Run: run-42", "trailers are the last paragraph, where git and the webhook parser read them")
	assert.Equal(t, "run-42", pubGit(t, f.remote, "log", "-1", "--format=%(trailers:key=Reliant-Run,valueonly)", "autopilot/issue-1"))
}

// Nothing to commit is not an error, and nothing is pushed.
func TestGitPublish_NoChangesMeansNoPushAndNoError(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	prep := prepare(t, f.clone, "nothing")
	assert.False(t, prep.Committed)
	assert.Equal(t, 0, prep.Commits, "a clean checkout whose commits are on the remote has nothing to publish")
	assert.Equal(t, pubGit(t, f.clone, "rev-parse", "HEAD"), prep.HeadSHA)
	assert.Empty(t, remoteHead(t, f, "autopilot/x"), "no push happened")
}

// A second publish of the same checkout finds nothing new.
func TestGitPublish_RepeatPublishHasNothingToPublish(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "add a")
	{
		r := push(t, f.pushReq("autopilot/x", prep.HeadSHA))
		require.True(t, r.Pushed, r.Refusal)
	}

	again := prepare(t, f.clone, "add a again")
	assert.False(t, again.Committed)
	assert.Equal(t, 0, again.Commits)
}

// Commits the agent made itself are published too, not only uncommitted changes.
func TestGitPublish_PublishesCommitsMadeBeforeTheNode(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	pubGit(t, f.clone, "add", "-A")
	pubGit(t, f.clone, "commit", "-q", "-m", "agent commit")

	prep := prepare(t, f.clone, "unused")
	assert.False(t, prep.Committed)
	assert.Equal(t, 1, prep.Commits)
	{
		r := push(t, f.pushReq("autopilot/x", prep.HeadSHA))
		require.True(t, r.Pushed, r.Refusal)
	}
}

// A diverged remote is refused, never overwritten: there is no force path.
func TestGitPublish_NeverForcePushes(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)

	// Someone else already published a different commit to the branch.
	other := filepath.Join(t.TempDir(), "other")
	pubGit(t, filepath.Dir(other), "clone", "-q", f.remote, other)
	require.NoError(t, os.WriteFile(filepath.Join(other, "theirs.txt"), []byte("theirs\n"), 0o644))
	pubGit(t, other, "add", "-A")
	pubGit(t, other, "commit", "-q", "-m", "theirs")
	pubGit(t, other, "push", "-q", "origin", "HEAD:refs/heads/autopilot/x")
	theirs := remoteHead(t, f, "autopilot/x")
	require.NotEmpty(t, theirs)

	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "ours.txt"), []byte("ours\n"), 0o644))
	prep := prepare(t, f.clone, "ours")
	res := push(t, f.pushReq("autopilot/x", prep.HeadSHA))

	assert.False(t, res.Pushed)
	assert.True(t, res.NonFastForward, res.Refusal)
	assert.Contains(t, res.Refusal, "not forced")
	assert.Equal(t, theirs, remoteHead(t, f, "autopilot/x"), "the remote branch is exactly what it was")
}

func TestGitPublish_RefusesDefaultProtectedAndOddBranches(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")
	before := remoteHead(t, f, "main")

	for _, branch := range []string{"main", "master", "release/v1", "release/v1/hotfix", "refs/heads/x", "refs/tags/v1", "-f", "a b", "x:main", "+x", "../x", ""} {
		res := push(t, f.pushReq(branch, prep.HeadSHA))
		assert.False(t, res.Pushed, "branch %q", branch)
		assert.NotEmpty(t, res.Refusal, "branch %q", branch)
	}
	assert.Equal(t, before, remoteHead(t, f, "main"))
	assert.Empty(t, remoteHead(t, f, "master"))
}

func TestGitPublish_RefusesAMovedCheckout(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")
	// Something committed after the node recorded its intent.
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "b.txt"), []byte("b\n"), 0o644))
	pubGit(t, f.clone, "add", "-A")
	pubGit(t, f.clone, "commit", "-q", "-m", "sneaked in")

	res := push(t, f.pushReq("autopilot/x", prep.HeadSHA))
	assert.False(t, res.Pushed)
	assert.Contains(t, res.Refusal, "moved on")
	assert.Empty(t, remoteHead(t, f, "autopilot/x"))
}

// Only the URL the server built is used: a remote the agent repointed, or a
// config that rewrites URLs or adds a credential helper, cannot capture a push.
func TestGitPublish_IgnoresTheCheckoutsRemotesAndRefusesDangerousConfig(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	evil := filepath.Join(t.TempDir(), "evil.git")
	pubGit(t, filepath.Dir(evil), "init", "-q", "--bare", evil)
	pubGit(t, f.clone, "remote", "set-url", "origin", "file://"+evil)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")

	{
		r := push(t, f.pushReq("autopilot/x", prep.HeadSHA))
		require.True(t, r.Pushed, r.Refusal)
	}
	assert.NotEmpty(t, remoteHead(t, f, "autopilot/x"), "went to the URL in the request")
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", "refs/heads/autopilot/x")
	cmd.Dir = evil
	assert.Error(t, cmd.Run(), "nothing reached the repointed origin")

	for _, kv := range [][2]string{
		{"url.file:///evil/.insteadOf", "file://"},
		{"credential.helper", "!echo spy"},
		{"credential.https://github.com.helper", "store"},
		{"http.proxy", "http://127.0.0.1:1"},
		{"http.https://github.com/.extraHeader", "X: y"},
	} {
		pubGit(t, f.clone, "config", kv[0], kv[1])
		res := push(t, f.pushReq("autopilot/y", prep.HeadSHA))
		assert.False(t, res.Pushed, kv[0])
		assert.Contains(t, res.Refusal, "git config", kv[0])
		pubGit(t, f.clone, "config", "--unset-all", kv[0])
	}
}

func TestGitPublish_RefusesDisallowedDestinations(t *testing.T) {
	needGit(t)
	f := newPublishFixture(t) // publishURLAllowed is the production check
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")
	for _, dest := range []string{
		"file://" + f.remote,
		"http://github.com/acme/app.git",
		"https://evil.example.com/acme/app.git",
		"https://github.com.evil.example.com/acme/app.git",
		"https://user:pw@github.com/acme/app.git",
		"https://github.com/acme/app",
		"https://github.com/acme/app/extra.git",
		"ssh://git@github.com/acme/app.git",
	} {
		req := f.pushReq("autopilot/x", prep.HeadSHA)
		req.RemoteURL = dest
		res := push(t, req)
		assert.False(t, res.Pushed, dest)
		assert.Contains(t, res.Refusal, "destination", dest)
	}
}

func TestGitPublish_RefusalsNeverContainTheToken(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	req := f.pushReq("main", "deadbeefdeadbeef")
	res := push(t, req)
	raw, _ := json.Marshal(res)
	assert.NotContains(t, string(raw), publishToken)
	req.RemoteURL = "file://" + f.remote + "/" + publishToken
	req.Branch = "autopilot/x"
	req.ExpectSHA = pubGit(t, f.clone, "rev-parse", "HEAD")
	raw, _ = json.Marshal(push(t, req))
	assert.NotContains(t, string(raw), publishToken, "an error that echoes a URL must not echo the token")
}

// ---- the credential ---------------------------------------------------------

// smartHTTP serves a bare repository over git's smart HTTP protocol and demands
// HTTP basic auth carrying publishToken. It records every Authorization header.
type smartHTTP struct {
	*httptest.Server
	mu    sync.Mutex
	auths []string
}

func newSmartHTTP(t *testing.T, remote string) *smartHTTP {
	t.Helper()
	backend, err := exec.LookPath("git-http-backend")
	if err != nil {
		out, gerr := exec.Command("git", "--exec-path").Output()
		require.NoError(t, gerr)
		backend = filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	}
	if _, err := os.Stat(backend); err != nil {
		t.Skip("git-http-backend not available")
	}
	pubGit(t, remote, "config", "http.receivepack", "true")
	h := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(remote), "GIT_HTTP_EXPORT_ALL=1"}}
	s := &smartHTTP{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := r.Header.Get("Authorization")
		s.mu.Lock()
		s.auths = append(s.auths, a)
		s.mu.Unlock()
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+publishToken))
		if a != want {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth required", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *smartHTTP) authenticated() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.auths {
		if a != "" {
			n++
		}
	}
	return n
}

// The credential reaches the remote through the pipe, and appears nowhere a
// shell or a file listing could find it.
func TestGitPublish_CredentialIsUsedOnceAndNeverLeaks(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	srv := newSmartHTTP(t, f.remote)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")

	// A hostile credential source the daemon's own environment offers: it must
	// not be used instead of, or alongside, the one-shot credential.
	t.Setenv("GH_TOKEN", "ghp_DAEMON_ENV_TOKEN")
	t.Setenv("GITHUB_TOKEN", "ghp_DAEMON_ENV_TOKEN")
	t.Setenv("GIT_ASKPASS", "/bin/false")

	before := pubSnapshot(t, os.TempDir(), f.clone, os.Getenv("HOME"))
	req := f.pushReq("autopilot/x", prep.HeadSHA)
	req.RemoteURL = srv.URL + "/remote.git"
	res := push(t, req)

	require.True(t, res.Pushed, res.Refusal)
	assert.NotEmpty(t, remoteHead(t, f, "autopilot/x"))
	assert.Positive(t, srv.authenticated(), "the remote saw the credential")
	for _, a := range srv.auths {
		assert.NotContains(t, a, "DAEMON_ENV_TOKEN", "the daemon's own environment was not offered")
	}

	// Nowhere on disk: not the repo's config, not the credential files, not temp.
	for path, content := range pubSnapshot(t, os.TempDir(), f.clone, os.Getenv("HOME")) {
		if before[path] == content {
			continue
		}
		assert.NotContains(t, content, publishToken, "the token was written to %s", path)
	}
	cfg := pubGit(t, f.clone, "config", "--local", "--list")
	assert.NotContains(t, cfg, publishToken)
	assert.NotContains(t, cfg, "x-access-token")
	assert.NotContains(t, pubGit(t, f.clone, "remote", "-v"), publishToken)
}

// A wrong credential is a refusal that says so, with nothing pushed.
func TestGitPublish_WrongCredentialIsRefused(t *testing.T) {
	needGit(t)
	allowFileRemotes(t)
	f := newPublishFixture(t)
	srv := newSmartHTTP(t, f.remote)
	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")
	req := f.pushReq("autopilot/x", prep.HeadSHA)
	req.RemoteURL = srv.URL + "/remote.git"
	req.Token = "ghs_wrong_token"
	res := push(t, req)
	assert.False(t, res.Pushed)
	assert.Contains(t, res.Refusal, "credential")
	assert.Empty(t, remoteHead(t, f, "autopilot/x"))
}

// While git runs, the credential is in no argv and no environment of any
// process on the machine. The "remote" is a hook-less server that holds the
// connection while the test inspects /proc.
func TestGitPublish_CredentialIsNotInArgvOrEnvironmentOfAnyProcess(t *testing.T) {
	needGit(t)
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("needs /proc")
	}
	allowFileRemotes(t)
	f := newPublishFixture(t)

	release := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		once.Do(func() { close(reached) })
		<-release
		http.Error(w, "closed", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)

	require.NoError(t, os.WriteFile(filepath.Join(f.clone, "a.txt"), []byte("a\n"), 0o644))
	prep := prepare(t, f.clone, "a")
	req := f.pushReq("autopilot/x", prep.HeadSHA)
	req.RemoteURL = srv.URL + "/remote.git"

	done := make(chan struct{})
	go func() { defer close(done); push(t, req) }()

	<-reached // git has authenticated: it holds the token right now
	leaks := scanProcsFor(t, publishToken)
	close(release)
	<-done
	assert.Empty(t, leaks, "the credential must not appear in any process's argv or environment")
}

func scanProcsFor(t *testing.T, needle string) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	require.NoError(t, err)
	var hits []string
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		for _, name := range []string{"cmdline", "environ"} {
			raw, err := os.ReadFile(filepath.Join("/proc", e.Name(), name))
			if err == nil && strings.Contains(string(raw), needle) {
				hits = append(hits, e.Name()+"/"+name)
			}
		}
	}
	return hits
}

// snapshotFiles reads small regular files under the given roots, depth-bounded.
func pubSnapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator)) > 6 {
					return filepath.SkipDir
				}
				return nil
			}
			if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() && fi.Size() < 1<<20 {
				if b, err := os.ReadFile(p); err == nil {
					out[p] = string(b)
				}
			}
			return nil
		})
	}
	return out
}
