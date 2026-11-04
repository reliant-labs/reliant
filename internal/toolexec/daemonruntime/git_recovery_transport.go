// Copyright (c) 2026 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
)

// credentialPipe never places the credential in argv, config, environment or a
// temporary file. A fresh pipe is required for EACH git invocation.
func credentialPipe(token string) (*os.File, error) {
	if token == "" || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, errors.New("invalid or missing git credential")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, errors.New("could not open private credential channel")
	}
	_, err = w.WriteString(token)
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		return nil, errors.New("could not stage git credential")
	}
	return r, nil
}

// path is empty for the existing publish protocol; recovery binds credentials
// to both HTTPS and the exact repository path, including reads and deletion.
func privateGitCredentialArgs(host, path string) []string {
	helper := `!f() { [ "$1" = get ] || return 0; h=; p=; s=; ` +
		`while IFS== read -r k v; do [ -z "$k" ] && break; case "$k" in host) h=$v;; path) p=$v;; protocol) s=$v;; esac; done; ` +
		`[ "$h" = "` + host + `" ] || return 0; `
	if path != "" {
		helper += `[ "$s" = https ] && [ "$p" = "` + path + `" ] || return 0; `
	}
	helper += `echo username=x-access-token; printf 'password=%s\n' "$(cat <&3)"; }; f`
	return []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "credential.helper=", "-c", "credential.helper=" + helper,
		"-c", fmt.Sprintf("credential.useHttpPath=%t", path != ""), "-c", "http.followRedirects=false", "-c", "http.proxy=", "-c", "http.extraHeader=", "-c", "http.sslVerify=true", "-c", "push.recurseSubmodules=no"}
}

var recoveryRepoName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}$`)
var recoveryOpaqueID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var recoveryOID = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// recoveryGitHubURL deliberately does not reuse publishURLAllowed: tests for
// publish may inject file URLs, but production recovery never accepts them.
func recoveryGitHubURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return nil, errors.New("recovery requires an explicit canonical GitHub HTTPS repository identity")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if len(parts) != 2 || !strings.HasSuffix(parts[1], ".git") {
		return nil, errors.New("invalid recovery repository identity")
	}
	for _, p := range []string{parts[0], strings.TrimSuffix(parts[1], ".git")} {
		if !recoveryRepoName.MatchString(p) || p == "." || p == ".." || strings.HasSuffix(p, ".") {
			return nil, errors.New("invalid recovery repository identity")
		}
	}
	return u, nil
}

func validateRecoveryTransport(c worktreereclaim.RecoveryCapture) error {
	if _, err := recoveryGitHubURL(c.RemoteURL); err != nil {
		return err
	}
	for _, id := range []string{c.WorktreeID, c.CaptureID, c.RepoID} {
		if !recoveryOpaqueID.MatchString(id) {
			return errors.New("invalid recovery opaque identity")
		}
	}
	if c.RecoveryRef != "refs/heads/reliant-recovery/"+c.WorktreeID+"/"+c.CaptureID+"/"+c.RepoID {
		return errors.New("recovery ref does not match capture identity")
	}
	if !recoveryOID.MatchString(c.CommitOID) || (c.ObjectFormat != "sha1" && c.ObjectFormat != "sha256") || (c.ObjectFormat == "sha1" && len(c.CommitOID) != 40) || (c.ObjectFormat == "sha256" && len(c.CommitOID) != 64) {
		return errors.New("invalid recovery object identity")
	}
	return nil
}

// recoveryTransport is private and concrete. Only tests inject a different
// endpoint/runner; the daemon commands always construct the production one.
type recoveryTransport struct {
	endpoint func(string) (*url.URL, error)
	run      func(context.Context, string, []string, string, bool, []*os.File, ...string) (pubGitResult, error)
	validate func(context.Context, string, worktreereclaim.RecoveryCapture) error
}

func productionRecoveryTransport() recoveryTransport {
	return recoveryTransport{recoveryGitHubURL, recoveryRunGit, worktreereclaim.ValidateRecoveryCapture}
}

// Cap untrusted remote diagnostics/advertisements, rather than collecting an
// unbounded buffer and truncating it after Git exits. Over-budget is a hold.
type recoveryOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *recoveryOutput) Len() int       { return b.buffer.Len() }
func (b *recoveryOutput) String() string { return b.buffer.String() }
func (b *recoveryOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.overflow = true
		return 0, errors.New("git output budget exceeded")
	}
	return b.buffer.Write(p)
}
func recoveryRunGit(ctx context.Context, dir string, env []string, stdin string, index bool, extra []*os.File, args ...string) (pubGitResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.ExtraFiles = extra
	var out, errb recoveryOutput
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if out.overflow || errb.overflow || ctx.Err() != nil {
		return pubGitResult{}, errors.New("git recovery budget exceeded")
	}
	res := pubGitResult{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.exit = ee.ExitCode()
		return res, nil
	}
	return res, err
}

// Source config is an untrusted execution surface. Recovery supports a narrow
// inert set only; a new Git feature must be assessed, not implicitly enabled.
func recoverySourceConfigSafe(ctx context.Context, dir string, env []string) bool {
	r, err := recoveryRunGit(ctx, dir, env, "", false, nil, "config", "--list", "--null")
	if err != nil || r.exit != 0 {
		return false
	}
	for _, entry := range strings.Split(r.stdout, "\x00") {
		if entry == "" {
			continue
		}
		key, _, ok := strings.Cut(entry, "\n")
		if !ok {
			return false
		}
		key = strings.ToLower(key)
		switch key {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "core.worktree", "user.name", "user.email", "extensions.objectformat", "extensions.worktreeconfig":
			continue
		}
		if strings.HasPrefix(key, "remote.") && (strings.HasSuffix(key, ".url") || strings.HasSuffix(key, ".pushurl") || strings.HasSuffix(key, ".fetch")) {
			continue
		}
		if strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge")) {
			continue
		}
		return false
	}
	return true
}

type recoverySession struct {
	transport recoveryTransport
	root      string
	env       []string
	endpoint  *url.URL
	token     string
}

func (t recoveryTransport) session(c worktreereclaim.RecoveryCapture, token string) (*recoverySession, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("private recovery git transport is unsupported on Windows")
	}
	if err := validateRecoveryTransport(c); err != nil {
		return nil, err
	}
	u, err := t.endpoint(c.RemoteURL)
	if err != nil {
		return nil, err
	}
	// Only the injected test endpoint may differ from the validated identity.
	root, err := os.MkdirTemp("", "reliant-recovery-proof-")
	if err != nil {
		return nil, errors.New("cannot create isolated recovery repository")
	}
	s := &recoverySession{transport: t, root: root, endpoint: u, token: token}
	// Do not inherit HOME/netrc, proxy, config, alternates, trace, askpass or
	// credentials. The git executable is selected through the daemon's PATH.
	for _, kv := range isolatedGitEnv() {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "SystemRoot=") {
			s.env = append(s.env, kv)
		}
	}
	s.env = append(s.env, "HOME="+root, "XDG_CONFIG_HOME="+root, "GIT_CEILING_DIRECTORIES="+root, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_ATTR_NOSYSTEM=1", "LC_ALL=C")
	r, err := credentialPipe(token)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	_ = r.Close()
	return s, nil
}
func (s *recoverySession) close() { _ = os.RemoveAll(s.root) }
func (s *recoverySession) git(ctx context.Context, dir string, network bool, args ...string) (pubGitResult, error) {
	base := []string{"--no-replace-objects", "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "core.attributesFile=" + os.DevNull, "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "fetch.fsckObjects=true", "-c", "transfer.fsckObjects=true", "-c", "fetch.recurseSubmodules=false", "-c", "gc.auto=0"}
	var extra []*os.File
	if network {
		r, err := credentialPipe(s.token)
		if err != nil {
			return pubGitResult{}, err
		}
		defer r.Close()
		extra = []*os.File{r}
		base = append(base, privateGitCredentialArgs(s.endpoint.Host, strings.TrimPrefix(s.endpoint.Path, "/"))...)
	}
	res, err := s.transport.run(ctx, dir, s.env, "", false, extra, append(base, args...)...)
	// Never return remote output: it may contain credentials, object contents or
	// attacker-controlled control characters. Detailed classification is bounded.
	if err != nil {
		return pubGitResult{}, errors.New("recovery git invocation failed; source retained")
	}
	return res, nil
}
func (s *recoverySession) must(ctx context.Context, dir string, network bool, args ...string) (string, error) {
	r, err := s.git(ctx, dir, network, args...)
	if err != nil {
		return "", err
	}
	if r.exit != 0 {
		return "", errors.New("recovery git operation refused or incomplete; check repository access, policy and connectivity; source retained")
	}
	return strings.TrimSpace(r.stdout), nil
}
func (s *recoverySession) lookup(ctx context.Context, c worktreereclaim.RecoveryCapture) (string, error) {
	out, err := s.must(ctx, s.root, true, "ls-remote", "--refs", s.endpoint.String(), c.RecoveryRef)
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", nil
	}
	fields := strings.Fields(out)
	if len(fields) != 2 || fields[1] != c.RecoveryRef || !recoveryOID.MatchString(fields[0]) {
		return "", errors.New("ambiguous recovery remote lookup")
	}
	return fields[0], nil
}
func (s *recoverySession) verify(ctx context.Context, c worktreereclaim.RecoveryCapture, consume func(string) error) (worktreereclaim.RecoveryReceipt, error) {
	var zero worktreereclaim.RecoveryReceipt
	if _, err := s.must(ctx, s.root, false, "check-ref-format", c.RecoveryRef); err != nil {
		return zero, err
	}
	bare := filepath.Join(s.root, "proof.git")
	if _, err := s.must(ctx, s.root, false, "init", "--bare", "--template=", "--object-format="+c.ObjectFormat, bare); err != nil {
		return zero, err
	}
	if _, err := s.must(ctx, bare, true, "fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", s.endpoint.String(), c.RecoveryRef+":refs/heads/proof"); err != nil {
		return zero, err
	}
	got, err := s.must(ctx, bare, false, "rev-parse", "--verify", "refs/heads/proof^{commit}")
	if err != nil {
		return zero, err
	}
	if got != c.CommitOID {
		return zero, errors.New("recovery remote ref differs from expected commit; source retained")
	}
	if _, err := s.must(ctx, bare, false, "fsck", "--full", "--strict", "--no-reflogs", c.CommitOID); err != nil {
		return zero, err
	}
	if err := s.transport.validate(ctx, bare, c); err != nil {
		return zero, errors.New("independent recovery manifest or promised closure validation failed; source retained")
	}
	if consume != nil {
		if err := consume(bare); err != nil {
			return zero, errors.New("verified recovery consumer failed; recovery ref retained")
		}
	}
	return worktreereclaim.RecoveryReceipt{Capture: c, VerifiedAt: time.Now().UTC(), VerifierVersion: "git-independent-v1"}, nil
}

// PublishGitRecovery creates only an absent recovery branch, or verifies an
// exact idempotent match. It never changes HEAD, index, local refs or disk files.
func PublishGitRecovery(ctx context.Context, sourcePath string, c worktreereclaim.RecoveryCapture, token string) (worktreereclaim.RecoveryReceipt, error) {
	return productionRecoveryTransport().publish(ctx, sourcePath, c, token)
}
func (t recoveryTransport) publish(ctx context.Context, sourcePath string, c worktreereclaim.RecoveryCapture, token string) (worktreereclaim.RecoveryReceipt, error) {
	var zero worktreereclaim.RecoveryReceipt
	s, err := t.session(c, token)
	if err != nil {
		return zero, err
	}
	defer s.close()
	if _, err := s.must(ctx, s.root, false, "check-ref-format", c.RecoveryRef); err != nil {
		return zero, err
	}
	if err := requireGitDir(sourcePath); err != nil {
		return zero, errors.New("recovery source is unavailable")
	}
	if !recoverySourceConfigSafe(ctx, sourcePath, s.env) {
		return zero, errors.New("source git configuration is unsafe for authenticated recovery")
	}
	// Never trust a policy string by itself: B validates the entire local
	// capture and publication envelope again before the FIRST remote call.
	if err := t.validate(ctx, sourcePath, c); err != nil {
		return zero, errors.New("recovery pre-egress validation failed; source retained")
	}
	current, err := s.lookup(ctx, c)
	if err != nil {
		return zero, err
	}
	if current != "" && current != c.CommitOID {
		return zero, errors.New("recovery ref conflict; never overwritten")
	}
	if current == "" {
		_, pushErr := s.must(ctx, sourcePath, true, "push", "--no-verify", "--no-recurse-submodules", "--porcelain", "--force-with-lease="+c.RecoveryRef+":", s.endpoint.String(), c.CommitOID+":"+c.RecoveryRef)
		// Lost responses and a concurrent identical create are safe to reconcile;
		// a different SHA is always a hold, never a rewrite.
		if pushErr != nil {
			got, e := s.lookup(ctx, c)
			if e != nil || got != c.CommitOID {
				return zero, pushErr
			}
		}
	}
	return s.verify(ctx, c, nil)
}
func VerifyGitRecovery(ctx context.Context, c worktreereclaim.RecoveryCapture, token string) (worktreereclaim.RecoveryReceipt, error) {
	return WithVerifiedGitRecovery(ctx, c, token, nil)
}

// WithVerifiedGitRecovery lends an isolated verified bare repository to restore
// for this callback only. No alternates or source objects are used. Ref retained.
func WithVerifiedGitRecovery(ctx context.Context, c worktreereclaim.RecoveryCapture, token string, consume func(string) error) (worktreereclaim.RecoveryReceipt, error) {
	s, err := productionRecoveryTransport().session(c, token)
	if err != nil {
		return worktreereclaim.RecoveryReceipt{}, err
	}
	defer s.close()
	return s.verify(ctx, c, consume)
}
func DeleteGitRecovery(ctx context.Context, c worktreereclaim.RecoveryCapture, token string) error {
	return productionRecoveryTransport().delete(ctx, c, token)
}
func (t recoveryTransport) delete(ctx context.Context, c worktreereclaim.RecoveryCapture, token string) error {
	s, err := t.session(c, token)
	if err != nil {
		return err
	}
	defer s.close()
	// Deletion needs no source clone, and never consults its config.
	if _, err := s.must(ctx, s.root, false, "check-ref-format", c.RecoveryRef); err != nil {
		return err
	}
	bare := filepath.Join(s.root, "delete.git")
	if _, err = s.must(ctx, s.root, false, "init", "--bare", "--template=", "--object-format="+c.ObjectFormat, bare); err != nil {
		return err
	}
	current, err := s.lookup(ctx, c)
	if err != nil {
		return err
	}
	if current == "" {
		return nil
	}
	if current != c.CommitOID {
		return errors.New("recovery ref changed; deletion refused")
	}
	_, pushErr := s.must(ctx, bare, true, "push", "--no-verify", "--no-recurse-submodules", "--porcelain", "--force-with-lease="+c.RecoveryRef+":"+c.CommitOID, s.endpoint.String(), ":"+c.RecoveryRef)
	got, err := s.lookup(ctx, c)
	if err != nil {
		return err
	}
	if got != "" {
		if pushErr != nil {
			return pushErr
		}
		return errors.New("recovery deletion not verified absent")
	}
	return nil
}
