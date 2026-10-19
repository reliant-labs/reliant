// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/gitpublish"
	"github.com/reliant-labs/reliant/internal/gitutil"
)

func init() {
	RegisterCommand(gitpublish.CommandPrepare, handleGitPublishPrepare)
	RegisterCommand(gitpublish.CommandPush, handleGitPublish)
}

// A git_publish node is two daemon commands so the credential exists for as
// short a time as it can.
//
//	git.publish_prepare  stages and commits the worktree's changes. No credential.
//	git.publish          pushes that commit, once.
//
// What the push never does, by construction:
//
//   - force: the refspec is <sha>:refs/heads/<branch> with no "+" and no
//     --force, so a remote that has moved on is refused, not overwritten;
//   - put the credential in an environment variable, an argument or a file: it
//     arrives in the command payload and is handed to the one git process over
//     a pipe it inherits as file descriptor 3, which a credential helper reads;
//   - use the checkout's remotes or its hooks: it pushes to an explicit URL the
//     server built, with hooks off, and it refuses a checkout whose own config
//     could redirect the push or spy on the credential.
//
// This is not containment. The pipe is readable by the daemon's own user for
// the instant the push runs, and a command run as that user can read the same
// credential files it always could.

// publishURLAllowed says where a push may go. Production is github.com over
// https; tests replace it to point at a local server.
var publishURLAllowed = func(u *url.URL) bool {
	return u.Scheme == "https" && strings.EqualFold(u.Host, "github.com") && u.User == nil &&
		strings.HasSuffix(u.Path, ".git") && strings.Count(strings.Trim(u.Path, "/"), "/") == 1
}

const (
	defaultAuthorName  = "Reliant"
	defaultAuthorEmail = "noreply@reliantlabs.io"
	// publishTrackingRef records what has been published, so a second publish of
	// an unchanged checkout has nothing to publish.
	publishTrackingNamespace = "refs/remotes/reliant-publish/"
	maxPublishOutput         = 1000
)

// isolatedGitEnv is the environment for every git process a publish starts: no
// credential variables, no prompt, and no user or system git config, so nothing
// outside the checkout can add a helper, rewrite a URL or set a proxy.
func isolatedGitEnv() []string {
	// Every GIT_* variable goes: GIT_DIR, GIT_WORK_TREE, GIT_EXEC_PATH and the
	// like change WHICH repository git acts on, or what it runs.
	var base []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
			base = append(base, kv)
		}
	}
	env := daemonpolicy.StripCredentials(base)
	return append(env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_EDITOR=true",
		"LC_ALL=C",
	)
}

type pubGitResult struct {
	stdout, stderr string
	exit           int
}

func (r pubGitResult) combined() string {
	return strings.TrimSpace(strings.TrimSpace(r.stdout) + "\n" + strings.TrimSpace(r.stderr))
}

// runGit runs git in dir. index selects the index-locking command path.
func pubRunGit(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
	var cmd *exec.Cmd
	if index {
		c, stop := gitutil.IndexCommand(ctx, dir, args...)
		defer stop()
		cmd = c
	} else {
		cmd = exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
	}
	cmd.Env = env
	cmd.ExtraFiles = extra
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := pubGitResult{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.exit = ee.ExitCode()
	default:
		return res, err
	}
	return res, nil
}

func requireGitDir(worktree string) error {
	if !filepath.IsAbs(worktree) {
		return fmt.Errorf("worktree must be an absolute path, got %q", worktree)
	}
	if fi, err := os.Stat(worktree); err != nil || !fi.IsDir() {
		return fmt.Errorf("worktree %q is not a directory", worktree)
	}
	return nil
}

// =============================================================================
// git.publish_prepare
// =============================================================================

func handleGitPublishPrepare(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitpublish.PrepareRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, fmt.Errorf("invalid payload: %w", err)
	}
	if err := requireGitDir(req.Worktree); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Message) == "" {
		return nil, errors.New("a commit message is required")
	}
	env := isolatedGitEnv()
	git := func(index bool, args ...string) (pubGitResult, error) {
		return pubRunGit(ctx, req.Worktree, env, "", index, nil, args...)
	}

	if res, err := git(false, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil || res.exit != 0 {
		return nil, errors.New("the worktree has no commits yet: nothing to build on")
	}

	if res, err := git(true, "add", "-A"); err != nil || res.exit != 0 {
		return nil, fmt.Errorf("git add failed: %s", firstNonEmpty(res.combined(), errString(err)))
	}
	staged, err := git(false, "diff", "--cached", "--quiet")
	if err != nil {
		return nil, err
	}
	committed := false
	if staged.exit == 1 {
		args := []string{
			"-c", "core.hooksPath=" + os.DevNull,
			"-c", "commit.gpgsign=false",
		}
		name, email := req.AuthorName, req.AuthorEmail
		if configured(ctx, req.Worktree, env, "user.name") == "" {
			args = append(args, "-c", "user.name="+firstNonEmpty(name, defaultAuthorName))
		}
		if configured(ctx, req.Worktree, env, "user.email") == "" {
			args = append(args, "-c", "user.email="+firstNonEmpty(email, defaultAuthorEmail))
		}
		args = append(args, "commit", "--no-verify", "-F", "-")
		res, err := pubRunGit(ctx, req.Worktree, env, commitMessage(req), true, nil, args...)
		if err != nil || res.exit != 0 {
			return nil, fmt.Errorf("git commit failed: %s", firstNonEmpty(res.combined(), errString(err)))
		}
		committed = true
	} else if staged.exit != 0 {
		return nil, fmt.Errorf("git diff failed: %s", staged.combined())
	}

	head, err := revParse(ctx, req.Worktree, env, "HEAD")
	if err != nil {
		return nil, err
	}
	resp := gitpublish.PrepareResponse{Committed: committed, HeadSHA: head}

	// Commits no remote has: what a push would publish. Published branches are
	// tracked under a namespace of their own, so a second publish of an
	// unchanged checkout finds nothing here.
	list, err := git(false, "rev-list", "--reverse", "HEAD", "--not", "--remotes")
	if err != nil || list.exit != 0 {
		return nil, fmt.Errorf("git rev-list failed: %s", firstNonEmpty(list.combined(), errString(err)))
	}
	var unpublished []string
	for _, l := range strings.Split(strings.TrimSpace(list.stdout), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			unpublished = append(unpublished, l)
		}
	}
	resp.Commits = len(unpublished)
	if resp.Commits > 0 {
		resp.BaseSHA = revParseQuiet(ctx, req.Worktree, env, unpublished[0]+"^")
		base := firstNonEmpty(resp.BaseSHA, emptyTree)
		resp.FilesChanged, resp.Insertions, resp.Deletions = diffStats(ctx, req.Worktree, env, base, head)
	}
	return json.Marshal(resp)
}

// emptyTree is git's empty tree, the base for a history whose first
// unpublished commit is a root commit.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func commitMessage(req gitpublish.PrepareRequest) string {
	msg := strings.TrimRight(strings.ReplaceAll(req.Message, "\r\n", "\n"), "\n ")
	var lines []string
	for _, t := range req.Trailers {
		key, val := sanitizeTrailerPart(t.Key), sanitizeTrailerPart(t.Value)
		if key == "" || val == "" {
			continue
		}
		lines = append(lines, key+": "+val)
	}
	if len(lines) == 0 {
		return msg + "\n"
	}
	// Trailers are the message's LAST paragraph, which is where git, and the
	// webhook parser's Reliant-Run reader, look for them.
	return msg + "\n\n" + strings.Join(lines, "\n") + "\n"
}

func sanitizeTrailerPart(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\n", " ", "\r", " ", ":", "-").Replace(s))
}

func configured(ctx context.Context, dir string, env []string, key string) string {
	res, err := pubRunGit(ctx, dir, env, "", false, nil, "config", "--get", key)
	if err != nil || res.exit != 0 {
		return ""
	}
	return strings.TrimSpace(res.stdout)
}

func revParse(ctx context.Context, dir string, env []string, rev string) (string, error) {
	res, err := pubRunGit(ctx, dir, env, "", false, nil, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || res.exit != 0 {
		return "", fmt.Errorf("cannot resolve %s: %s", rev, firstNonEmpty(res.combined(), errString(err)))
	}
	return strings.TrimSpace(res.stdout), nil
}

func revParseQuiet(ctx context.Context, dir string, env []string, rev string) string {
	sha, err := revParse(ctx, dir, env, rev)
	if err != nil {
		return ""
	}
	return sha
}

func diffStats(ctx context.Context, dir string, env []string, base, head string) (files, ins, del int) {
	res, err := pubRunGit(ctx, dir, env, "", false, nil, "diff", "--numstat", base, head)
	if err != nil || res.exit != 0 {
		return 0, 0, 0
	}
	for _, line := range strings.Split(strings.TrimSpace(res.stdout), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		files++
		if n, err := strconv.Atoi(f[0]); err == nil {
			ins += n
		}
		if n, err := strconv.Atoi(f[1]); err == nil {
			del += n
		}
	}
	return files, ins, del
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// =============================================================================
// git.publish
// =============================================================================

// dangerousConfig matches configuration in the checkout that could redirect a
// push or hand the credential to someone else: a rewritten URL, a credential
// helper of the repository's own (git offers a successful credential to EVERY
// configured helper to store). The user's and the system's config are never
// read for a publish, so this only ever sees the repository's own.
//
// http.* is included because a URL-scoped http.<url>.proxy or extraHeader beats
// the general reset the push applies.
var dangerousConfig = regexp.MustCompile(`^(url\..*|credential(\..*)?\.[a-z]+|http\..*|core\.(gitproxy|sshcommand|askpass))$`)

// hostPattern is what may appear as the host the credential helper answers for.
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)

func handleGitPublish(ctx context.Context, payload []byte) ([]byte, error) {
	var req gitpublish.PushRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		// The error text is the parser's, never the payload: it holds a credential.
		return nil, errors.New("invalid payload")
	}
	token := req.Token
	req.Token = ""
	refuse := func(format string, args ...any) ([]byte, error) {
		return json.Marshal(gitpublish.PushResponse{Refusal: scrub(fmt.Sprintf(format, args...), token)})
	}

	if runtime.GOOS == "windows" {
		return refuse("publishing is not supported on Windows machines yet: the credential cannot be handed to git privately there")
	}
	if token == "" {
		return refuse("no credential was supplied")
	}
	if err := requireGitDir(req.Worktree); err != nil {
		return refuse("%v", err)
	}
	if !gitpublish.ValidBranch(req.Branch) || gitpublish.NeverPublishable(req.Branch) {
		return refuse("%q is not a branch a publish may push to", req.Branch)
	}
	u, err := url.Parse(req.RemoteURL)
	if err != nil || !publishURLAllowed(u) {
		return refuse("the push destination is not an allowed GitHub repository URL")
	}
	// The credential helper answers only for this host. A URL with no host (a
	// local path, in tests) has no credential to give.
	if u.Host != "" && !hostPattern.MatchString(u.Host) {
		return refuse("the push destination is not an allowed GitHub repository URL")
	}
	if len(req.ExpectSHA) < 7 {
		return refuse("no commit to push was named")
	}

	env := isolatedGitEnv()
	head, err := revParse(ctx, req.Worktree, env, "HEAD")
	if err != nil {
		return refuse("%v", err)
	}
	if head != req.ExpectSHA {
		return refuse("the checkout moved on after it was prepared (head %s, expected %s): nothing was pushed", short(head), short(req.ExpectSHA))
	}
	if key := dangerousKey(ctx, req.Worktree, env); key != "" {
		return refuse("the checkout's git config sets %s, which could redirect the push or read the credential: nothing was pushed", key)
	}

	// The credential rides a pipe the push inherits as fd 3. Written and closed
	// here, before the process exists: a pipe buffers it, so there is no writer
	// to race, and nothing to leave behind.
	r, w, err := os.Pipe()
	if err != nil {
		return refuse("could not open a private channel for the credential")
	}
	defer r.Close()
	_, werr := w.WriteString(token)
	_ = w.Close()
	if werr != nil {
		return refuse("could not stage the credential for the push")
	}

	helper := `!f() { [ "$1" = get ] || return 0; h=; ` +
		`while IFS== read -r k v; do [ -z "$k" ] && break; [ "$k" = host ] && h=$v; done; ` +
		`[ "$h" = "` + u.Host + `" ] || return 0; ` +
		`echo username=x-access-token; printf 'password=%s\n' "$(cat <&3)"; }; f`
	args := []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "credential.helper=",
		"-c", "credential.helper=" + helper,
		"-c", "credential.useHttpPath=false",
		"-c", "http.followRedirects=false",
		"-c", "http.proxy=",
		"-c", "http.extraHeader=",
		"-c", "http.sslVerify=true",
		"-c", "push.recurseSubmodules=no",
		"push", "--no-verify", "--no-recurse-submodules", "--porcelain",
		req.RemoteURL, req.ExpectSHA + ":refs/heads/" + req.Branch,
	}
	res, err := pubRunGit(ctx, req.Worktree, env, "", false, []*os.File{r}, args...)
	if err != nil {
		return refuse("could not run git: %v", err)
	}
	out := scrub(res.combined(), token)

	if res.exit != 0 {
		return json.Marshal(gitpublish.PushResponse{Refusal: classifyPushFailure(out), NonFastForward: nonFastForward(out)})
	}
	// --porcelain prints one "<flag>\t<from>:<to>\t<summary>" line per ref:
	// '=' is up to date; ' ' (fast-forward), '*' (new) and '+' are updates.
	pushed := false
	for _, line := range strings.Split(res.stdout, "\n") {
		if len(line) >= 2 && line[1] == '\t' && strings.ContainsRune(" *+", rune(line[0])) {
			pushed = true
		}
	}
	if !pushed {
		return json.Marshal(gitpublish.PushResponse{})
	}
	// Record what was published, so the next prepare of this checkout finds
	// nothing new. Best effort: a missing tracking ref only makes a repeat
	// publish push the same commit again, which the remote reports as up to date.
	_, _ = pubRunGit(ctx, req.Worktree, env, "", false, nil, "update-ref", publishTrackingNamespace+req.Branch, req.ExpectSHA)
	return json.Marshal(gitpublish.PushResponse{Pushed: true})
}

// dangerousKey returns the first configured key in the checkout that could
// redirect a push or capture a credential, or "".
func dangerousKey(ctx context.Context, dir string, env []string) string {
	res, err := pubRunGit(ctx, dir, env, "", false, nil, "config", "--list", "--null")
	if err != nil {
		return "git config could not be read"
	}
	for _, entry := range strings.Split(res.stdout, "\x00") {
		key, _, _ := strings.Cut(entry, "\n")
		if dangerousConfig.MatchString(strings.ToLower(key)) {
			return key
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func scrub(s, token string) string {
	if len(token) >= 4 {
		s = strings.ReplaceAll(s, token, "[redacted]")
	}
	return s
}

func nonFastForward(out string) bool {
	lo := strings.ToLower(out)
	return strings.Contains(lo, "non-fast-forward") || strings.Contains(lo, "fetch first") ||
		strings.Contains(lo, "tip of your current branch is behind") || strings.Contains(lo, "stale info")
}

func classifyPushFailure(out string) string {
	lo := strings.ToLower(out)
	switch {
	case nonFastForward(out):
		return "the remote branch has commits this checkout does not (non-fast-forward): the push was refused, not forced"
	case strings.Contains(lo, "authentication failed") || strings.Contains(lo, "could not read username") ||
		strings.Contains(lo, "terminal prompts disabled") || strings.Contains(lo, "invalid username or password"):
		return "the remote did not accept the credential"
	case strings.Contains(lo, "protected branch") || strings.Contains(lo, "gh006") || strings.Contains(lo, "gh013"):
		return "the remote refused the push: a protection rule applies to this branch"
	case strings.Contains(lo, "permission") && strings.Contains(lo, "denied"), strings.Contains(lo, "403"):
		return "the remote refused the push: the credential has no write access to this repository"
	}
	if len(out) > maxPublishOutput {
		out = out[len(out)-maxPublishOutput:]
	}
	return "the push failed: " + strings.TrimSpace(out)
}
