package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func webhookUnreachable(detail string) error {
	return fmt.Errorf("applying database: %w", apierrors.NewInternalError(errors.New(
		`failed calling webhook "database.example": failed to call webhook: `+detail)))
}

func TestIsTransient(t *testing.T) {
	resource := schema.GroupResource{Group: "forge.dev", Resource: "databases"}
	refused := &url.Error{Op: "Post", URL: "https://10.0.0.1:443", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}
	for name, err := range map[string]error{
		"conflict":              apierrors.NewConflict(resource, "db", errors.New("modified")),
		"webhook unreachable":   webhookUnreachable("connection refused"),
		"server timeout":        apierrors.NewServerTimeout(resource, "update", 1),
		"request timeout":       apierrors.NewTimeoutError("timed out", 1),
		"server unavailable":    apierrors.NewServiceUnavailable("unavailable"),
		"connection refused":    refused,
		"dropped connection":    &url.Error{Op: "Get", URL: "https://x", Err: io.EOF},
		"context deadline":      context.DeadlineExceeded,
		"network timeout":       timeoutError{},
		"http2 connection lost": errors.New("http2: client connection lost"),
	} {
		t.Run(name, func(t *testing.T) {
			if !IsTransient(err) {
				t.Fatalf("IsTransient(%v) = false, want true", err)
			}
		})
	}
	for name, err := range map[string]error{
		"plain error":    errors.New("invalid spec"),
		"invalid object": apierrors.NewInvalid(schema.GroupKind{Group: "forge.dev", Kind: "Database"}, "db", nil),
		"forbidden":      apierrors.NewForbidden(resource, "db", errors.New("denied")),
		"not found":      apierrors.NewNotFound(resource, "db"),
		"webhook denial": apierrors.NewInternalError(errors.New("webhook denied the request")),
	} {
		t.Run(name, func(t *testing.T) {
			if IsTransient(err) {
				t.Fatalf("IsTransient(%v) = true, want false", err)
			}
		})
	}
}

// relayedStorageError is the API server's answer when etcd fails a request
// with an error the API server has no Status for: a 500 of unknown reason
// whose message is the etcd client's error text (apiserver ErrorToAPIStatus).
func relayedStorageError(message string) error {
	return &apierrors.StatusError{ErrStatus: metav1.Status{
		Status: metav1.StatusFailure, Code: http.StatusInternalServerError,
		Reason: metav1.StatusReasonUnknown, Message: message,
	}}
}

// TestIsTransient_Throttling is the regression test for control-plane's
// workspace reconciler reporting ~20 errors in one burst of API-server/etcd
// throttling (2026-10-09), each one cleared by the next requeue:
//
//	ensuring user namespace: checking namespace ws-…: rpc error: code =
//	ResourceExhausted desc = stream terminated by RST_STREAM with error
//	code: ENHANCE_YOUR_CALM
//
// IsTransient read them as permanent, so each was reported at once instead of
// riding out TransientGrace. Every throttling shape is wrapped the way a
// reconciler wraps it, because that is how IsTransient receives it.
func TestIsTransient_Throttling(t *testing.T) {
	resource := schema.GroupResource{Resource: "namespaces"}
	wrap := func(err error) error {
		return fmt.Errorf("ensuring user namespace: checking namespace ws-1: %w", err)
	}
	for name, err := range map[string]error{
		"etcd RST_STREAM relayed by the API server": wrap(relayedStorageError(
			"rpc error: code = ResourceExhausted desc = stream terminated by RST_STREAM with error code: ENHANCE_YOUR_CALM")),
		"etcd too many requests relayed":          wrap(relayedStorageError("etcdserver: too many requests")),
		"etcd too many requests as InternalError": wrap(apierrors.NewInternalError(errors.New("etcdserver: too many requests"))),
		"etcd unavailable relayed":                wrap(relayedStorageError("rpc error: code = Unavailable desc = etcdserver: leader changed")),
		"etcd request timed out relayed":          wrap(relayedStorageError("etcdserver: request timed out")),
		"API server too many requests":            wrap(apierrors.NewTooManyRequests("throttled", 1)),
		"API server 429 response":                 wrap(apierrors.NewGenericServerResponse(http.StatusTooManyRequests, "get", resource, "ws-1", "", 1, true)),
		"API server server timeout":               wrap(apierrors.NewServerTimeout(resource, "get", 1)),
		"API server request timeout":              wrap(apierrors.NewTimeoutError("timed out", 1)),
		"gRPC ResourceExhausted":                  wrap(status.Error(codes.ResourceExhausted, "etcdserver: too many requests")),
		"gRPC Unavailable":                        wrap(status.Error(codes.Unavailable, "connection closed")),
		"HTTP/2 ENHANCE_YOUR_CALM":                &url.Error{Op: "Get", URL: "https://10.0.0.1:443", Err: http2.StreamError{StreamID: 3, Code: http2.ErrCodeEnhanceYourCalm}},
		"HTTP/2 REFUSED_STREAM":                   wrap(http2.StreamError{StreamID: 5, Code: http2.ErrCodeRefusedStream}),
	} {
		t.Run(name, func(t *testing.T) {
			if !IsTransient(err) {
				t.Fatalf("IsTransient(%v) = false, want true", err)
			}
		})
	}

	// The throttling text is a retry signal only on a SERVER-side failure,
	// and a gRPC status only for the codes that mean "not now".
	for name, err := range map[string]error{
		"invalid quoting the text": wrap(apierrors.NewInvalid(schema.GroupKind{Kind: "Namespace"}, "ws-1", nil)),
		"forbidden quoting the text": wrap(apierrors.NewForbidden(resource, "ws-1",
			errors.New("rpc error: code = ResourceExhausted"))),
		"gRPC InvalidArgument":     wrap(status.Error(codes.InvalidArgument, "bad spec")),
		"gRPC PermissionDenied":    wrap(status.Error(codes.PermissionDenied, "denied")),
		"HTTP/2 PROTOCOL_ERROR":    wrap(http2.StreamError{StreamID: 1, Code: http2.ErrCodeProtocol}),
		"unrelated internal error": wrap(apierrors.NewInternalError(errors.New("nil pointer in admission"))),
	} {
		t.Run(name, func(t *testing.T) {
			if IsTransient(err) {
				t.Fatalf("IsTransient(%v) = true, want false", err)
			}
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timed out" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recordingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Clone())
	return nil
}
func (h *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(string) slog.Handler      { return h }
func (h *recordingHandler) messages(level slog.Level) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var messages []string
	for _, record := range h.records {
		if record.Level == level {
			messages = append(messages, record.Message)
		}
	}
	return messages
}

type transientHarness struct {
	reconciler *Reconciler[*corev1.ConfigMap]
	logs       *recordingHandler
	clock      time.Time
}

func newTransientHarness(t *testing.T) *transientHarness {
	t.Helper()
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "default"}}
	client := fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(configMap).Build()
	harness := &transientHarness{
		logs:  &recordingHandler{},
		clock: time.Date(2026, 10, 8, 17, 42, 19, 0, time.UTC),
	}
	harness.reconciler = &Reconciler[*corev1.ConfigMap]{Client: client, Log: slog.New(harness.logs)}
	harness.reconciler.now = func() time.Time { return harness.clock }
	return harness
}

func (h *transientHarness) run(t *testing.T, err error) (Result, error) {
	t.Helper()
	return h.reconciler.Run(context.Background(),
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(context.Context, *corev1.ConfigMap) (Result, error) { return Done(), err }, nil)
}

func TestRunTransientErrorUsesBackoffUntilGrace(t *testing.T) {
	harness := newTransientHarness(t)
	failure := webhookUnreachable("connection refused")
	var previous time.Duration
	for attempt := range 4 {
		result, err := harness.run(t, failure)
		if err != nil {
			t.Fatalf("attempt %d returned %v", attempt, err)
		}
		if result.RequeueAfter <= previous {
			t.Fatalf("attempt %d retry = %s, previous = %s", attempt, result.RequeueAfter, previous)
		}
		previous = result.RequeueAfter
		harness.clock = harness.clock.Add(result.RequeueAfter)
	}
	if errors := harness.logs.messages(slog.LevelError); len(errors) != 0 {
		t.Fatalf("transient blip logged at error: %v", errors)
	}
}

func TestRunPersistentTransientErrorIsReported(t *testing.T) {
	harness := newTransientHarness(t)
	failure := webhookUnreachable("no endpoints available")
	if _, err := harness.run(t, failure); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	harness.clock = harness.clock.Add(DefaultTransientGrace)
	if _, err := harness.run(t, failure); !errors.Is(err, failure) {
		t.Fatalf("after grace Run error = %v, want %v", err, failure)
	}
	if got := harness.logs.messages(slog.LevelError); len(got) != 1 {
		t.Fatalf("persistent transient errors logged %d times, want once: %v", len(got), got)
	}
}

func TestRunSuccessResetsTransientStreak(t *testing.T) {
	harness := newTransientHarness(t)
	failure := webhookUnreachable("connection refused")
	if _, err := harness.run(t, failure); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	harness.clock = harness.clock.Add(time.Second)
	if _, err := harness.run(t, nil); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	harness.clock = harness.clock.Add(time.Hour)
	if _, err := harness.run(t, failure); err != nil {
		t.Fatalf("new transient streak was reported: %v", err)
	}
}

func TestRunPermanentErrorIsReportedImmediately(t *testing.T) {
	harness := newTransientHarness(t)
	failure := errors.New("invalid spec")
	if _, err := harness.run(t, failure); !errors.Is(err, failure) {
		t.Fatalf("Run error = %v, want %v", err, failure)
	}
	if got := harness.logs.messages(slog.LevelError); len(got) != 1 || got[0] != "reconcile failed" {
		t.Fatalf("error logs = %v, want reconcile failed", got)
	}
}

func TestRunNegativeTransientGraceReportsImmediately(t *testing.T) {
	harness := newTransientHarness(t)
	harness.reconciler.TransientGrace = -1
	failure := webhookUnreachable("connection refused")
	if _, err := harness.run(t, failure); !errors.Is(err, failure) {
		t.Fatalf("Run error = %v, want %v", err, failure)
	}
}
