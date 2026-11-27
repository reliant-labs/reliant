// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
	"github.com/reliant-labs/reliant/internal/mergequeue/candidate"
)

// The merge queue's code operations (internal/mergequeue/buildjob): building
// a batch's candidate, listing a head's changed paths and reading
// .reliant/pipeline.yaml at a commit. The server holds no repository bytes
// (owner, 2026-11-27), so it sends these to a member of the repository's
// gate pool, which runs them over a git cache of its own
// (<appdata>/queue-jobs) and reports back ids, paths and bytes the server
// checks. The gate's own commands (cmd_gate.go) stay as they are.
//
// The credential is the Reliant App's installation token for the one
// repository, contents:write and pull_requests:read, minted for this
// machine's daemon session at the moment a job needs it (`reliant github
// token --as app`, which control-plane mints only when the user can push to
// the repository). It is held in memory, handed to git through the child's
// environment for a fetch or a push, and never written anywhere. The regen
// command runs without it, in the credential sandbox.
func init() {
	for _, c := range []string{buildjob.CommandStart, buildjob.CommandStatus, buildjob.CommandCancel} {
		RegisterCommand(c, handlePipelineJobCommand(c))
	}
}

var pipelineJobs = sync.OnceValue(func() *candidate.Host {
	return &candidate.Host{
		Root:        filepath.Join(config.GetAppDataDir(), "queue-jobs"),
		Credentials: &queueTokens{},
		Runner:      candidate.SandboxRunner{},
	}
})

func handlePipelineJobCommand(command string) CommandHandler {
	return func(ctx context.Context, payload []byte) ([]byte, error) {
		return pipelineJobs().Handle(ctx, command, payload)
	}
}

// queueTokenPermissions are what a pipeline job's token may do: fetch and
// push (contents), and read a pull request back (pull_requests).
var queueTokenPermissions = []string{"contents=write", "pull_requests=read"}

// queueTokenTimeout bounds one mint; it matches `reliant github token`'s own.
const queueTokenTimeout = 90 * time.Second

// queueTokens mints and keeps (in memory, until shortly before they expire)
// the pipeline jobs' installation tokens, one per repository.
type queueTokens struct {
	mu     sync.Mutex
	tokens map[string]queueToken
	// mint is the minting command; nil is mintQueueToken.
	mint func(ctx context.Context, repository string) (queueToken, error)
	now  func() time.Time
}

type queueToken struct {
	token   string
	expires time.Time
}

// queueTokenSlack is how long before its expiry a token is no longer handed
// out: a build's fetches and pushes all happen within it.
const queueTokenSlack = 10 * time.Minute

// Token implements candidate.Credentials.
func (q *queueTokens) Token(ctx context.Context, repository string) (string, error) {
	now := time.Now
	if q.now != nil {
		now = q.now
	}
	key := strings.ToLower(repository)
	q.mu.Lock()
	if t, ok := q.tokens[key]; ok && now().Add(queueTokenSlack).Before(t.expires) {
		q.mu.Unlock()
		return t.token, nil
	}
	q.mu.Unlock()
	mint := q.mint
	if mint == nil {
		mint = mintQueueToken
	}
	t, err := mint(ctx, repository)
	if err != nil {
		return "", err
	}
	if t.expires.IsZero() {
		// An expiry the server did not say: an installation token lives an
		// hour, and this one is used for no more than its first half.
		t.expires = now().Add(30*time.Minute + queueTokenSlack)
	}
	q.mu.Lock()
	if q.tokens == nil {
		q.tokens = map[string]queueToken{}
	}
	q.tokens[key] = t
	q.mu.Unlock()
	return t.token, nil
}

// queueTokenArgv is the command that mints a pipeline job's token: this
// binary's `github token --as app`, pinned to the daemon session the git
// credential helper is pinned to.
func queueTokenArgv(repository string) []string {
	helper := gitCredentialHelperArgv()
	if len(helper) == 0 {
		return nil
	}
	argv := []string{helper[0], "github", "token", "--as", "app", "--repo", repository, "--json"}
	for _, p := range queueTokenPermissions {
		argv = append(argv, "--permission", p)
	}
	// The helper's own arguments after `auth git-credential` are the session
	// pin, which `github token` takes under the same flag names.
	if len(helper) >= 3 && helper[1] == "auth" && helper[2] == "git-credential" {
		argv = append(argv, helper[3:]...)
	}
	return argv
}

func mintQueueToken(ctx context.Context, repository string) (queueToken, error) {
	argv := queueTokenArgv(repository)
	if len(argv) == 0 {
		return queueToken{}, errors.New("this daemon cannot find its own binary to mint a GitHub token with")
	}
	ctx, cancel := context.WithTimeout(ctx, queueTokenTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = os.Environ() //nolint:forbidigo // the first-party CLI child finds this machine's daemon session through the daemon's own environment; not configuration
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[len(msg)-500:]
		}
		return queueToken{}, fmt.Errorf("mint a GitHub App token for %s: %w: %s", repository, err, msg)
	}
	var doc struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil || doc.Token == "" {
		return queueToken{}, fmt.Errorf("mint a GitHub App token for %s: the answer carried no token", repository)
	}
	t := queueToken{token: doc.Token}
	if exp, err := time.Parse(time.RFC3339, doc.ExpiresAt); err == nil {
		t.expires = exp
	}
	return t, nil
}
