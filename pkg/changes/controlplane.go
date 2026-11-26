package changes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// The environment variables of an app NOT on Reliant hosting, which fetches
// its environment's snapshot from the control plane instead of reading a
// mounted file. OpenFromEnv reads all three (EnvFile wins when it is set):
//
//	FORGE_CHANGES_URL             the control plane's base URL
//	FORGE_CHANGES_ENVIRONMENT_ID  the environment whose snapshot to fetch
//	FORGE_CHANGES_TOKEN           an access token carrying changes:read
//
// Keep the token in the app's secret store, never in a repository, and never
// in anything a browser loads.
const (
	EnvURL           = "FORGE_CHANGES_URL"
	EnvEnvironmentID = "FORGE_CHANGES_ENVIRONMENT_ID"
	EnvToken         = "FORGE_CHANGES_TOKEN"
)

// DefaultFetchInterval is how often a Store fetches from the control plane
// when Options names no interval.
const DefaultFetchInterval = 15 * time.Second

// DefaultFetchTimeout bounds one fetch when ControlPlaneSource names no
// timeout.
const DefaultFetchTimeout = 10 * time.Second

// maxFetchBackoff caps how long failing fetches back off: a control plane
// that comes back is seen again within about this long.
const maxFetchBackoff = 5 * time.Minute

// maxResponseBytes bounds a snapshot response. The largest valid declaration
// (200 changes of 32 variants of 8 KiB) is about 50 MiB.
const maxResponseBytes = 64 << 20

// snapshotProcedure is the Connect procedure that serves an environment's
// snapshot.
const snapshotProcedure = "/controlplane.v1.ChangesService/GetChangesSnapshot"

// ControlPlaneSource says where OpenControlPlane fetches the snapshot from.
type ControlPlaneSource struct {
	// BaseURL is the control plane's base URL, such as
	// https://api.reliantlabs.io. Required.
	BaseURL string
	// EnvironmentID is the environment whose snapshot to fetch. Required.
	EnvironmentID string
	// Token is an access token carrying changes:read, ideally bound to this
	// environment. Required. It is sent as a bearer token and never logged.
	Token string
	// HTTPClient sends the request. Nil is a client of its own, traced
	// through pkg/observe.
	HTTPClient *http.Client
	// Timeout bounds one fetch. Zero is DefaultFetchTimeout.
	Timeout time.Duration
}

// OpenControlPlane returns a Store over the environment's snapshot as the
// control plane delivers it (ChangesService.GetChangesSnapshot), for an app
// that is not on Reliant hosting. It fetches once before returning (waiting
// at most the source's Timeout), then every PollInterval (default
// DefaultFetchInterval) until ctx is done or Close is called.
//
// Like OpenFile it never fails. A source missing a required field is an
// empty store, with one warning. A fetch that fails (unreachable, a refused
// token, a server error) keeps the last good snapshot, warns once per
// distinct error and backs off, jittered, up to about five minutes between
// attempts. A response whose etag has not moved is not parsed again; one
// that does not parse, or that carries a revision older than the last good
// snapshot's, is refused and the last good snapshot kept.
func OpenControlPlane(ctx context.Context, cp ControlPlaneSource, opts Options) *Store {
	s, ok := newControlPlaneStore(ctx, cp, opts)
	if !ok {
		return s
	}
	t := time.NewTicker(s.interval)
	s.start(ctx, t.C, t.Stop)
	return s
}

// newControlPlaneStore builds an unstarted Store over cp. When cp cannot be
// fetched it warns and returns an empty, closed Store and false.
func newControlPlaneStore(ctx context.Context, cp ControlPlaneSource, opts Options) (*Store, bool) {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultFetchInterval
	}
	src, err := cp.source()
	if err != nil {
		s := newStore(nil, opts)
		s.logger.WarnContext(ctx, "changes: cannot fetch the snapshot; every change evaluates to the default in code", slog.String("error", err.Error()))
		close(s.done)
		return s, false
	}
	s := newStore(src, opts)
	s.maxSkip = int((maxFetchBackoff + s.interval - 1) / s.interval)
	return s, true
}

func (cp ControlPlaneSource) source() (*controlPlaneSource, error) {
	var missing []string
	if strings.TrimSpace(cp.BaseURL) == "" {
		missing = append(missing, "BaseURL")
	}
	if strings.TrimSpace(cp.EnvironmentID) == "" {
		missing = append(missing, "EnvironmentID")
	}
	if strings.TrimSpace(cp.Token) == "" {
		missing = append(missing, "Token")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("ControlPlaneSource needs %s", strings.Join(missing, ", "))
	}
	base := strings.TrimRight(strings.TrimSpace(cp.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("ControlPlaneSource.BaseURL %q is not an http(s) URL", cp.BaseURL)
	}
	body, err := json.Marshal(map[string]string{"environmentId": strings.TrimSpace(cp.EnvironmentID)})
	if err != nil {
		return nil, err
	}
	client := cp.HTTPClient
	if client == nil {
		// Traced like every outbound call of the app; the per-fetch
		// deadline is the source's Timeout.
		client = observe.NewHTTPClient(observe.HTTPClientOptions{})
	}
	timeout := cp.Timeout
	if timeout <= 0 {
		timeout = DefaultFetchTimeout
	}
	return &controlPlaneSource{
		endpoint:      base + snapshotProcedure,
		environmentID: strings.TrimSpace(cp.EnvironmentID),
		token:         strings.TrimSpace(cp.Token),
		body:          body,
		client:        client,
		timeout:       timeout,
	}, nil
}

// controlPlaneSource fetches GetChangesSnapshot as a Connect unary call over
// JSON: no generated client, so forge needs nothing of the control plane's
// but the wire shape.
type controlPlaneSource struct {
	endpoint      string
	environmentID string
	token         string
	body          []byte
	client        *http.Client
	timeout       time.Duration
}

// snapshotResponse is the part of GetChangesSnapshotResponse a Store reads.
// The revision field is the snapshot's own, which ParseSnapshot reads; the
// problems are the console's to show.
type snapshotResponse struct {
	SnapshotJSON string `json:"snapshotJson"`
	Etag         string `json:"etag"`
}

// connectError is a Connect error body.
type connectError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *controlPlaneSource) read(ctx context.Context) sourceRead {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(c.body))
	if err != nil {
		return fetchFailed("changes: cannot build the snapshot request; keeping the last good snapshot", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return fetchFailed("changes: cannot reach the control plane; keeping the last good snapshot", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err == nil && len(data) > maxResponseBytes {
		err = fmt.Errorf("the response is larger than %d bytes", maxResponseBytes)
	}
	if err != nil {
		return fetchFailed("changes: cannot read the control plane's response; keeping the last good snapshot", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fetchFailed(statusWarning(resp.StatusCode), statusError(resp.StatusCode, data))
	}
	var wire snapshotResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return fetchFailed("changes: cannot decode the control plane's response; keeping the last good snapshot", err)
	}
	if wire.SnapshotJSON == "" {
		return sourceRead{missing: true, msg: "changes: the control plane delivered no snapshot; every change evaluates to the default in code"}
	}
	version := wire.Etag
	if version == "" {
		sum := sha256.Sum256([]byte(wire.SnapshotJSON))
		version = hex.EncodeToString(sum[:])
	}
	return sourceRead{data: []byte(wire.SnapshotJSON), version: version}
}

func (c *controlPlaneSource) noun() string { return "the control plane's snapshot" }

func (c *controlPlaneSource) attrs() []any {
	return []any{slog.String("url", c.endpoint), slog.String("environment_id", c.environmentID)}
}

func (c *controlPlaneSource) monotonic() bool { return true }

func fetchFailed(msg string, err error) sourceRead {
	return sourceRead{err: err, msg: msg, backoff: true}
}

// statusWarning says what a refusal means for the person reading the log.
func statusWarning(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "changes: the control plane refused the token (it needs changes:read on this environment); keeping the last good snapshot"
	case http.StatusNotFound:
		return "changes: the control plane serves no snapshot for this environment; keeping the last good snapshot"
	default:
		return "changes: the control plane did not serve the snapshot; keeping the last good snapshot"
	}
}

// statusError renders a non-200 response: its status and, when the body is a
// Connect error, its code and the first line of its message.
func statusError(status int, body []byte) error {
	var ce connectError
	if json.Unmarshal(body, &ce) == nil && ce.Code != "" {
		msg, _, _ := strings.Cut(ce.Message, "\n")
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return fmt.Errorf("HTTP %d %s: %s", status, ce.Code, msg)
	}
	return fmt.Errorf("HTTP %d %s", status, http.StatusText(status))
}

// OpenFromEnv opens the Store an app's environment declares, reading it
// through getenv (pass os.Getenv; this package never reads the environment
// itself):
//
//   - EnvFile set: OpenFile over that path. Reliant hosting sets it.
//   - else EnvURL, EnvEnvironmentID and EnvToken all set: OpenControlPlane.
//   - else none of them: an empty store, where every call site gets the
//     default written in code.
//
// Some but not all of the three fetch variables is an empty store too, with
// one warning naming the missing ones. Like OpenFile, it never fails:
//
//	store := changes.OpenFromEnv(ctx, os.Getenv, changes.Options{})
//	defer store.Close()
func OpenFromEnv(ctx context.Context, getenv func(string) string, opts Options) *Store {
	if getenv == nil {
		return OpenFile(ctx, "", opts)
	}
	if path := strings.TrimSpace(getenv(EnvFile)); path != "" {
		return OpenFile(ctx, path, opts)
	}
	cp := ControlPlaneSource{
		BaseURL:       strings.TrimSpace(getenv(EnvURL)),
		EnvironmentID: strings.TrimSpace(getenv(EnvEnvironmentID)),
		Token:         strings.TrimSpace(getenv(EnvToken)),
	}
	var missing []string
	for name, v := range map[string]string{EnvURL: cp.BaseURL, EnvEnvironmentID: cp.EnvironmentID, EnvToken: cp.Token} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	switch len(missing) {
	case 0:
		return OpenControlPlane(ctx, cp, opts)
	case 3:
		return OpenFile(ctx, "", opts)
	}
	slices.Sort(missing)
	s := OpenFile(ctx, "", opts)
	s.logger.WarnContext(ctx, "changes: the control plane fetch is half configured; every change evaluates to the default in code",
		slog.String("missing", strings.Join(missing, ", ")))
	return s
}
