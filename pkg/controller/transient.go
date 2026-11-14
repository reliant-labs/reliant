package controller

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilnet "k8s.io/apimachinery/pkg/util/net"
)

// IsTransient reports whether a reconcile error is one the next attempt can be
// expected to clear without anybody changing anything.
//
// The distinction it draws is the one an operator's error reporting has to
// draw. A reconcile that failed because an admission webhook's pod was
// restarting, because the informer cache was a write behind the API server, or
// because the API server shed load, failed for a reason that is gone by the
// next requeue. Reporting each of those as an error pages someone for a blip
// the controller was already recovering from. A reconcile that failed because
// the spec is invalid, a referenced object is missing, or Kubernetes RBAC
// denies the write will fail identically next time, and that is the failure
// worth reporting.
//
// Classified by type wherever the error carries one:
//
//   - optimistic-concurrency conflicts ("the object has been modified");
//   - the API server's own retry signals: ServerTimeout, Timeout (504),
//     TooManyRequests and ServiceUnavailable;
//   - transport failures between this process and an API server: connection
//     refused or reset, an EOF on a dropped connection, a network timeout;
//   - context cancellation or deadline, which is the manager shutting down or
//     a call outliving its budget;
//   - a gRPC status of ResourceExhausted or Unavailable anywhere in the chain:
//     a gRPC backend shedding load or briefly unreachable;
//   - an HTTP/2 stream the server reset with ENHANCE_YOUR_CALM or
//     REFUSED_STREAM, its way of telling a client to back off.
//
// And two text matches, for failures the API server reports without a type of
// their own:
//
//   - an admission webhook the API server could not call is an InternalError
//     whose message reads `failed calling webhook "<name>": ...`;
//   - etcd throttling the API server. The API server relays an etcd failure
//     it has no Status for as a 500 whose message is the etcd client's gRPC
//     error text — `rpc error: code = ResourceExhausted desc = stream
//     terminated by RST_STREAM with error code: ENHANCE_YOUR_CALM`, or
//     `etcdserver: too many requests`. Nothing typed survives the hop, so
//     the message is all there is (see isRelayedStorageThrottling).
//
// "Transient" is a claim about one attempt, not about the condition. A webhook
// misconfigured for good also fails to be called, which is why Reconciler.Run
// reports a transient error that has persisted past TransientGrace.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch {
	case apierrors.IsConflict(err),
		apierrors.IsServerTimeout(err),
		apierrors.IsTimeout(err),
		apierrors.IsTooManyRequests(err),
		apierrors.IsServiceUnavailable(err):
		return true
	}
	if isWebhookCallFailure(err) || isRelayedStorageThrottling(err) {
		return true
	}
	if isGRPCBackoff(err) || isHTTP2Backoff(err) {
		return true
	}
	if utilnet.IsConnectionRefused(err) || utilnet.IsConnectionReset(err) ||
		utilnet.IsProbableEOF(err) || utilnet.IsHTTP2ConnectionLost(err) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

const webhookCallFailure = "failed calling webhook"

func isWebhookCallFailure(err error) bool {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	result := status.Status()
	return result.Reason == metav1.StatusReasonInternalError && strings.Contains(result.Message, webhookCallFailure)
}

// storageThrottlingText are the fragments of an etcd client error that mean
// "back off and retry": the gRPC codes etcd's client reports for a server
// shedding load or briefly unreachable, the HTTP/2 reset behind the first, and
// etcd's own overload and leader-election errors.
var storageThrottlingText = []string{
	"code = ResourceExhausted",
	"code = Unavailable",
	"ENHANCE_YOUR_CALM",
	"etcdserver: too many requests",
	"etcdserver: request timed out",
	"etcdserver: leader changed",
}

// isRelayedStorageThrottling reports whether err is an API-server error
// relaying etcd throttling. Only a server-side failure qualifies — a 500, or
// the InternalError / unknown reason the API server gives an error it has no
// Status for — so a validation or permission error that happens to quote one
// of these strings is never read as a retry signal.
func isRelayedStorageThrottling(err error) bool {
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	result := status.Status()
	serverSide := result.Code == http.StatusInternalServerError ||
		result.Reason == metav1.StatusReasonInternalError || result.Reason == metav1.StatusReasonUnknown
	if !serverSide {
		return false
	}
	for _, text := range storageThrottlingText {
		if strings.Contains(result.Message, text) {
			return true
		}
	}
	return false
}

// isGRPCBackoff reports whether err carries a gRPC status of ResourceExhausted
// or Unavailable, the codes a gRPC server uses for "not now". status.FromError
// finds a status wrapped anywhere in the chain.
func isGRPCBackoff(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st == nil {
		return false
	}
	switch st.Code() {
	case codes.ResourceExhausted, codes.Unavailable:
		return true
	}
	return false
}

// isHTTP2Backoff reports whether err is an HTTP/2 stream the server reset to
// make the client back off.
func isHTTP2Backoff(err error) bool {
	var stream http2.StreamError
	if !errors.As(err, &stream) {
		return false
	}
	return stream.Code == http2.ErrCodeEnhanceYourCalm || stream.Code == http2.ErrCodeRefusedStream
}

// DefaultTransientGrace is how long Reconciler.Run tolerates one object's
// reconcile failing with transient errors before it reports them.
const DefaultTransientGrace = 5 * time.Minute

// transientBackoff paces requeues within a transient streak. It starts short
// for brief recoveries and caps at a minute, avoiding a fixed rapid retry loop.
var transientBackoff = Backoff{Initial: 500 * time.Millisecond, Max: time.Minute, Factor: 2}

// streakSet records each object's current run of transient failures. A streak
// is cleared on success, permanent failure, or deletion.
type streakSet struct {
	mu      sync.Mutex
	streaks map[types.NamespacedName]streak
}

type streak struct {
	first    time.Time
	attempts int
}

func (s *streakSet) record(key types.NamespacedName, now time.Time) (attempts int, since time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.streaks[key]
	if !ok {
		current = streak{first: now}
	}
	current.attempts++
	s.streaks[key] = current
	return current.attempts, now.Sub(current.first)
}

func (s *streakSet) clear(key types.NamespacedName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.streaks, key)
}

var streakSetInit sync.Mutex
