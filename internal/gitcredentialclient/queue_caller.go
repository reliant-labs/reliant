package gitcredentialclient

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The worker's proof, on a Queue App mint, that it is the reliant worker
// (control-plane internal/auth/queue_mint_caller.go): an HS256 JWT signed with
// GITHUB_QUEUE_APP_CALLER_SECRET, which only the worker and the process that
// mints hold. The names below are a cross-repo contract; control-plane pins
// them in TestWireJSON_QueueAppMatchesReliantClient.
const (
	// QueueCallerHeader carries the assertion.
	QueueCallerHeader = "X-Reliant-Queue-Caller"
	// QueueCallerSubject and QueueCallerAudience are the only sub and aud a
	// queue mint admits.
	QueueCallerSubject  = "reliant-worker"
	QueueCallerAudience = "control-plane/github-queue-mint"
	// QueueCallerSecretMinLen is the shortest secret control-plane accepts.
	QueueCallerSecretMinLen = 32
	// queueCallerLifetime is exp - iat: control-plane refuses more than ten
	// minutes, and the worker signs a fresh one per call.
	queueCallerLifetime = 5 * time.Minute
	// queueAppEnum is GithubApp.GITHUB_APP_QUEUE as Connect JSON names it.
	queueAppEnum = "GITHUB_APP_QUEUE"
)

// ErrQueueCallerSecret: the secret is too short to sign with.
var ErrQueueCallerSecret = errors.New("gitcredentialclient: GITHUB_QUEUE_APP_CALLER_SECRET is shorter than 32 bytes")

// SignQueueCallerAssertion signs a fresh caller assertion with secret.
func SignQueueCallerAssertion(secret string, now time.Time) (string, error) {
	if len(secret) < QueueCallerSecretMinLen {
		return "", ErrQueueCallerSecret
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": QueueCallerSubject,
		"aud": QueueCallerAudience,
		"iat": now.Unix(),
		"exp": now.Add(queueCallerLifetime).Unix(),
	}).SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("gitcredentialclient: signing the queue caller assertion: %w", err)
	}
	return signed, nil
}
