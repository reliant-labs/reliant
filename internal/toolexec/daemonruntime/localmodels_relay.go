// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/localprobe"
	"github.com/reliant-labs/reliant/internal/logging"
	"google.golang.org/protobuf/proto"
)

const (
	localModelRefreshInterval = 60 * time.Second
	localRelayMaxConcurrent   = 4
	localRelayDefaultIdle     = 120 * time.Second
	localRelayFlushInterval   = 50 * time.Millisecond
	localRelayFlushBytes      = 32 * 1024
	localRelayReadBuf         = 16 * 1024
)

// localModelManager owns the daemon's local-model inventory and relay state.
type localModelManager struct {
	prober *localModelProber
	client *http.Client // relay transport: no overall timeout, idle handled per request

	mu        sync.Mutex
	snap      *localInventorySnapshot
	published *reliantv1.LocalModelInventory // last inventory sent this session

	registered chan struct{}
	refresh    chan struct{}

	relayMu sync.Mutex
	relays  map[string]context.CancelFunc
	slots   chan struct{}
}

func newLocalModelManager() *localModelManager {
	return &localModelManager{
		prober:     newLocalModelProber(),
		client:     &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		registered: make(chan struct{}, 1),
		refresh:    make(chan struct{}, 1),
		relays:     make(map[string]context.CancelFunc),
		slots:      make(chan struct{}, localRelayMaxConcurrent),
	}
}

func (d *daemonClient) localModelMgr() *localModelManager {
	d.localModelsOnce.Do(func() {
		d.localModels = newLocalModelManager()
		activeLocalModels.Store(d.localModels)
	})
	return d.localModels
}

// signalRegistered tells the publisher the gateway accepted this session.
func (m *localModelManager) signalRegistered() {
	select {
	case m.registered <- struct{}{}:
	default:
	}
}

func (m *localModelManager) requestRefresh() {
	select {
	case m.refresh <- struct{}{}:
	default:
	}
}

func (m *localModelManager) current() *localInventorySnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

// inventoryEqual compares two inventories ignoring probed_at.
func inventoryEqual(a, b *reliantv1.LocalModelInventory) bool {
	if a == nil || b == nil {
		return a == b
	}
	ac, bc := proto.Clone(a).(*reliantv1.LocalModelInventory), proto.Clone(b).(*reliantv1.LocalModelInventory)
	ac.ProbedAt, bc.ProbedAt = "", ""
	return proto.Equal(ac, bc)
}

// probeAndPublish re-probes and sends the inventory when it changed (or force).
func (m *localModelManager) probeAndPublish(ctx context.Context, send func(*reliantv1.DaemonMessage) error, force bool) {
	snap := m.prober.probe(ctx)
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	m.snap = snap
	changed := force || !inventoryEqual(m.published, snap.inventory)
	m.mu.Unlock()
	if !changed {
		return
	}
	msg := &reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_LocalModelInventory{LocalModelInventory: snap.inventory}}
	if err := send(msg); err != nil {
		logging.Warn(logPrefix+" Failed to send local model inventory", "error", err)
		return
	}
	m.mu.Lock()
	m.published = snap.inventory
	m.mu.Unlock()
}

// run is the per-session publisher: it waits for registration, publishes
// once, then re-probes on a timer and on demand, sending only on change.
func (m *localModelManager) run(ctx context.Context, send func(*reliantv1.DaemonMessage) error) {
	m.mu.Lock()
	m.published = nil
	m.mu.Unlock()
	select {
	case <-m.registered:
	default:
	}
	select {
	case <-m.registered:
	case <-ctx.Done():
		return
	}
	m.probeAndPublish(ctx, send, true)
	ticker := time.NewTicker(localModelRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.probeAndPublish(ctx, send, false)
		case <-m.refresh:
			// An explicit refresh always answers, even when nothing changed.
			m.probeAndPublish(ctx, send, true)
		}
	}
}

// ---- request path / body rules ----

// resolveLocalRelayPath maps a requested path (relative to the endpoint's
// /v1 base, or one of the root-level native paths) onto the upstream path and
// enforces the allow-list. The result is always cleaned, so ".." cannot
// escape the allowed prefixes.
func resolveLocalRelayPath(requested string) (upstreamPath, query string, err error) {
	p := requested
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p, query = p[:i], p[i+1:]
	}
	if !strings.HasPrefix(p, "/") {
		return "", "", fmt.Errorf("path must start with /")
	}
	decoded, uerr := url.PathUnescape(p)
	if uerr != nil || strings.ContainsAny(decoded, "\\\x00") {
		return "", "", fmt.Errorf("invalid path")
	}
	for _, seg := range strings.Split(decoded, "/") {
		if seg == ".." {
			return "", "", fmt.Errorf("path %q is not allowed", requested)
		}
	}
	cleaned := path.Clean(decoded)
	if !isRootLocalPath(cleaned) && cleaned != "/v1" && !strings.HasPrefix(cleaned, "/v1/") && !strings.HasPrefix(cleaned, "/api/") {
		// Relative to the endpoint's /v1 base (e.g. "/chat/completions").
		cleaned = "/v1" + cleaned
	}
	if !localPathAllowed(cleaned) {
		return "", "", fmt.Errorf("path %q is not allowed", requested)
	}
	return cleaned, query, nil
}

func isRootLocalPath(p string) bool {
	return p == "/api/show" || p == "/api/tags" || p == "/props" || p == "/api/v0" || strings.HasPrefix(p, "/api/v0/")
}

func localPathAllowed(p string) bool {
	return p == "/v1" || strings.HasPrefix(p, "/v1/") || isRootLocalPath(p)
}

var localHopByHop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true,
	"te": true, "trailer": true, "trailers": true, "transfer-encoding": true, "upgrade": true,
	"host": true, "content-length": true, "authorization": true, "accept-encoding": true,
}

// ---- relay ----

// handleLocalModelHTTPRequest authorizes and starts a relay. It never blocks
// the caller's message loop: all work, including waiting for a slot, happens
// on a goroutine.
func (d *daemonClient) handleLocalModelHTTPRequest(ctx context.Context, req *reliantv1.LocalModelHTTPRequest) {
	m := d.localModelMgr()
	go m.relay(ctx, d.send, req)
}

func (d *daemonClient) handleLocalModelHTTPCancel(c *reliantv1.LocalModelHTTPCancel) {
	d.localModelMgr().cancelRelay(c.GetRequestId())
}

func (d *daemonClient) handleLocalModelRefresh() {
	d.localModelMgr().requestRefresh()
}

func (m *localModelManager) cancelRelay(requestID string) {
	m.relayMu.Lock()
	cancel := m.relays[requestID]
	m.relayMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type relayWriter struct {
	send    func(*reliantv1.DaemonMessage) error
	reqID   string
	seq     uint64
	aborted bool
}

func (w *relayWriter) emit(c *reliantv1.LocalModelHTTPChunk) bool {
	c.RequestId = w.reqID
	c.Sequence = w.seq
	w.seq++
	err := w.send(&reliantv1.DaemonMessage{Message: &reliantv1.DaemonMessage_LocalModelHttpChunk{LocalModelHttpChunk: c}})
	if err != nil {
		w.aborted = true
	}
	return err == nil
}

func (w *relayWriter) fail(msg string) {
	w.emit(&reliantv1.LocalModelHTTPChunk{Done: true, Error: msg})
}

func (m *localModelManager) relay(parent context.Context, send func(*reliantv1.DaemonMessage) error, req *reliantv1.LocalModelHTTPRequest) {
	w := &relayWriter{send: send, reqID: req.GetRequestId()}
	if w.reqID == "" {
		return
	}

	snap := m.current()
	if snap == nil {
		w.fail("local model inventory not ready")
		return
	}
	tgt, ok := snap.targets[req.GetEndpointId()]
	if !ok {
		w.fail(fmt.Sprintf("unknown local model endpoint %q", req.GetEndpointId()))
		return
	}
	upPath, query, err := resolveLocalRelayPath(req.GetPath())
	if err != nil {
		w.fail(err.Error())
		return
	}
	method := strings.ToUpper(strings.TrimSpace(req.GetMethod()))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodPost && method != http.MethodHead {
		w.fail(fmt.Sprintf("method %s is not allowed", method))
		return
	}

	body := req.GetBody()

	target := tgt.root + upPath
	if query != "" {
		target += "?" + query
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	m.relayMu.Lock()
	if _, dup := m.relays[w.reqID]; dup {
		m.relayMu.Unlock()
		w.fail("duplicate request id")
		return
	}
	m.relays[w.reqID] = cancel
	m.relayMu.Unlock()
	defer func() {
		m.relayMu.Lock()
		delete(m.relays, w.reqID)
		m.relayMu.Unlock()
	}()

	// Queue behind other relays; cancellation while queued ends it here.
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		w.fail("canceled")
		return
	}

	idle := localRelayDefaultIdle
	if ms := req.GetIdleTimeoutMs(); ms > 0 {
		idle = time.Duration(ms) * time.Millisecond
	}
	var timedOut atomic.Bool
	idleTimer := time.AfterFunc(idle, func() { timedOut.Store(true); cancel() })
	defer idleTimer.Stop()

	httpReq, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		w.fail(err.Error())
		return
	}
	for k, v := range req.GetHeaders() {
		if !localHopByHop[strings.ToLower(k)] {
			httpReq.Header.Set(k, v)
		}
	}
	if tgt.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+tgt.apiKey)
	}
	if len(body) > 0 && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := m.client.Do(httpReq)
	if err != nil {
		w.fail(relayErrString(ctx, &timedOut, idle, err))
		return
	}
	defer resp.Body.Close()
	idleTimer.Reset(idle)

	headers := make(map[string]string, len(resp.Header))
	for k, vs := range resp.Header {
		if !localHopByHop[strings.ToLower(k)] {
			headers[k] = strings.Join(vs, ", ")
		}
	}
	if !w.emit(&reliantv1.LocalModelHTTPChunk{Status: int32(resp.StatusCode), Headers: headers}) {
		return
	}

	type readResult struct {
		data []byte
		err  error
	}
	reads := make(chan readResult)
	go func() {
		defer close(reads)
		for {
			buf := make([]byte, localRelayReadBuf)
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				select {
				case reads <- readResult{data: buf[:n]}:
				case <-ctx.Done():
					return
				}
			}
			if rerr != nil {
				select {
				case reads <- readResult{err: rerr}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()

	var pending []byte
	lastFlush := time.Now()
	flush := func() bool {
		if len(pending) == 0 {
			return true
		}
		ok := w.emit(&reliantv1.LocalModelHTTPChunk{Data: pending})
		pending = nil
		lastFlush = time.Now()
		return ok
	}
	flushTimer := time.NewTimer(time.Hour)
	flushTimer.Stop()
	defer flushTimer.Stop()

	for {
		select {
		case r, open := <-reads:
			if !open {
				if ctx.Err() != nil {
					flush()
					w.fail(relayErrString(ctx, &timedOut, idle, ctx.Err()))
					return
				}
				flush()
				w.emit(&reliantv1.LocalModelHTTPChunk{Done: true})
				return
			}
			if len(r.data) > 0 {
				idleTimer.Reset(idle)
				pending = append(pending, r.data...)
				if len(pending) >= localRelayFlushBytes || time.Since(lastFlush) >= localRelayFlushInterval {
					if !flush() {
						return
					}
				} else {
					flushTimer.Reset(localRelayFlushInterval - time.Since(lastFlush))
				}
			}
			if r.err != nil {
				if !flush() {
					return
				}
				if r.err == io.EOF {
					w.emit(&reliantv1.LocalModelHTTPChunk{Done: true})
				} else {
					w.fail(relayErrString(ctx, &timedOut, idle, r.err))
				}
				return
			}
		case <-flushTimer.C:
			if !flush() {
				return
			}
		case <-ctx.Done():
			flush()
			w.fail(relayErrString(ctx, &timedOut, idle, ctx.Err()))
			return
		}
	}
}

func relayErrString(ctx context.Context, timedOut *atomic.Bool, idle time.Duration, err error) string {
	switch {
	case timedOut.Load():
		return fmt.Sprintf("local model server sent no data for %s", idle)
	case ctx.Err() != nil:
		return "canceled"
	default:
		return localprobe.ShortError(err)
	}
}
