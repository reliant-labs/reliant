// Copyright (c) 2025 Reliant Labs

// Package daemonstate owns the tools daemon's on-disk runtime record.
//
// In dial-out mode the daemon binds no port of its own, so nothing on the
// machine can be probed to learn whether it is doing any work. "A process
// exists" and "the gateway stream is established" are different claims, and a
// daemon that never connected looks identical from the outside to one that is
// serving tool calls. This record is how the daemon publishes the difference
// to `reliant daemon status` and `reliant daemon stop`.
//
// It also records which binary the process is running (path, mtime, VCS
// revision, dirty flag) so a stale daemon left over from an earlier session —
// serving a run with code that predates every fix you are trying to measure —
// is visible instead of indistinguishable.
package daemonstate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// FileName is the runtime record's basename inside the daemon's data dir.
const FileName = "daemon-state.json"

// Stream is the daemon's gateway-stream state as the daemon itself sees it.
type Stream string

const (
	// StreamUnknown means the record carries no stream state at all. Callers
	// must report it as unknown — never as "running".
	StreamUnknown Stream = ""
	// StreamConnecting means a dial is in flight; the gateway has not yet
	// acknowledged registration.
	StreamConnecting Stream = "connecting"
	// StreamConnected means the gateway acknowledged registration on the
	// current stream. This is the only state in which the daemon can serve
	// tool calls.
	StreamConnected Stream = "connected"
	// StreamDisconnected means the stream ended (or never came up). Detail
	// carries the error that ended it.
	StreamDisconnected Stream = "disconnected"
	// StreamListening is the server-mode analogue of connecting: the daemon is
	// accepting gateway dial-ins but none is attached.
	StreamListening Stream = "listening"
	// StreamAwaitingCredentials means the daemon has no usable credentials and
	// (running non-interactively, e.g. spawned by Electron) is idling rather
	// than exiting or opening an interactive login flow. It is waiting for a
	// credential to appear on disk — normally because Electron's own login UI
	// signed the user in and minted one. Electron reads this exact string from
	// `stream` in daemon-state.json to distinguish "waiting for sign-in" from
	// a crash, instead of reporting a timeout after waitForReady's 30s.
	StreamAwaitingCredentials Stream = "awaiting_credentials"
)

// StreamNoticePrefix marks a line on the daemon's stdout announcing a stream
// transition, e.g. `@@RELIANT_STREAM connected`.
//
// This is the PUSH half of how a supervising Electron learns the daemon is up.
// The runtime record in daemon-state.json remains the source of truth — a
// remote daemon has no stdout its supervisor can read, and the file is what
// `reliant daemon status` reads — but a locally spawned daemon already has an
// ordered, authenticated pipe to its parent, and announcing on it turns a
// discovery that cost up to a full poll interval into an immediate one.
//
// Measured on the dev stack: Electron's fallback fs.watchFile stats the record
// every 250ms, so it learned of a connect 146ms after the fact, purely from
// landing mid-interval. Nothing was slow; the supervisor was simply asking
// rather than being told.
//
// The prefix is deliberately unlikely to occur in ordinary log output, because
// the reader must never mistake a log line for a notice. Electron mirrors this
// constant in electron/src/daemon-contract.js.
const StreamNoticePrefix = "@@RELIANT_STREAM"

// Established reports whether the daemon can currently serve tool calls.
func (s Stream) Established() bool { return s == StreamConnected }

// State is the daemon's runtime record.
type State struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`

	// Executable / BinaryModTime / Revision / Dirty identify the build this
	// process is running, so a stale binary is visible without guesswork.
	Executable    string    `json:"executable,omitempty"`
	BinaryModTime time.Time `json:"binary_mod_time,omitempty"`
	Revision      string    `json:"revision,omitempty"`
	Dirty         bool      `json:"dirty,omitempty"`

	GatewayURL string `json:"gateway_url,omitempty"`
	TLSMode    string `json:"tls_mode,omitempty"`
	ServerMode bool   `json:"server_mode,omitempty"`

	// Instance names the daemon instance this record belongs to: the
	// "<origin>/<sub>/<workspace>" slug from internal/daemoninstance. It is
	// what makes the record self-describing, so a reader that expects one
	// instance can reject a record belonging to another instead of trusting
	// whatever JSON happens to sit at a path and acting on its PID.
	//
	// Empty when the data dir is not an instance directory — a container with
	// an explicit DAEMON_DATA_DIR=/data has no instance identity to claim, and
	// inventing one would be a lie rather than a default. Readers must treat
	// "" as "unknown", never as "mine".
	//
	// Electron mirrors this field name ("instance") when it verifies ownership.
	Instance string `json:"instance,omitempty"`

	Stream          Stream    `json:"stream"`
	StreamChangedAt time.Time `json:"stream_changed_at"`
	// ConnectedAt is the last time the gateway acknowledged registration. Zero
	// if it never has — the discriminator between "flapping" and "never up".
	ConnectedAt time.Time `json:"connected_at,omitempty"`
	// StreamDetail carries the error that ended the last stream, if any.
	StreamDetail string `json:"stream_detail,omitempty"`

	// Sessions counts how many times the stream has reached "connected", and
	// LastDisconnectAt is the last time it left. Together they are what makes a
	// SINGLE read of this record able to answer "is the stream stable?" rather
	// than only "is it up at this instant".
	//
	// A single instantaneous sample is close to meaningless against a stream
	// that flaps: on 2026-07-27 six `daemon status` samples 12s apart all said
	// "connected", for 15s / 27s / 8s / 20s / 7s / 0s — every one of them a
	// different session, and the check was used as a pre-flight that pronounced
	// a known-bad daemon safe to start a 30-minute run against.
	Sessions         int       `json:"sessions,omitempty"`
	LastDisconnectAt time.Time `json:"last_disconnect_at,omitempty"`
}

// StabilityWindow is how recently the stream must have been unbroken for it to
// count as stable. The daemon's reconnect backoff caps at 15s and its heartbeat
// is 15s, so a full flap cycle is at most ~30s; two clean cycles is the
// shortest window that cannot be satisfied by a flapping stream.
const StabilityWindow = 2 * time.Minute

// Stable reports whether the stream is established AND has not dropped inside
// StabilityWindow. Callers that gate work on the daemon (pre-flight checks,
// long runs) must use this, not Stream.Established().
func (s State) Stable(now time.Time) bool {
	if !s.Stream.Established() {
		return false
	}
	if s.LastDisconnectAt.IsZero() {
		return true
	}
	return now.Sub(s.LastDisconnectAt) >= StabilityWindow
}

// mu serializes the read-modify-write in SetStream. The record has exactly one
// writing process (the daemon itself); `status` and `stop` only read it.
var mu sync.Mutex

// Path returns the record's location inside dataDir.
func Path(dataDir string) string { return filepath.Join(dataDir, FileName) }

// InstanceFor derives the instance slug a record at dataDir belongs to, or ""
// when dataDir is not an instance directory.
//
// The identity is DERIVED from the directory rather than passed in alongside
// it, because an instance dir already encodes its own key: daemoninstance
// projects (origin, sub, workspace) onto exactly three path segments under
// ~/.reliant/instances. Taking the caller's word for it instead would admit a
// whole bug class — a record that claims one instance while sitting in
// another's directory — and that mismatch is precisely what readers are about
// to start trusting. Derivation cannot disagree with the filesystem.
//
// Both sides are resolved through their deepest existing ancestor before being
// compared, so a symlinked prefix (macOS's /var -> /private/var, which every
// t.TempDir inherits) does not make a directory look foreign to itself.
func InstanceFor(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	root, err := instancesRoot()
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(resolveThroughSymlinks(root), resolveThroughSymlinks(dataDir))
	if err != nil {
		return ""
	}
	segments := strings.Split(rel, string(filepath.Separator))
	if len(segments) != 3 {
		return ""
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
	}
	// "/" regardless of OS: the slug is a stable identifier shared with
	// Electron and with log fields, not a filesystem path.
	return strings.Join(segments, "/")
}

// resolveThroughSymlinks makes a path absolute and resolves symlinks in the
// part of it that exists, keeping the not-yet-created tail verbatim.
//
// filepath.EvalSymlinks fails outright on a path whose leaf does not exist, so
// resolving only when it happens to succeed would resolve one side of a
// comparison and not the other — which is how a directory ends up looking like
// it belongs to somebody else.
func resolveThroughSymlinks(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	var tail []string
	current := abs
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(append([]string{resolved}, tail...)...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs
		}
		tail = append([]string{filepath.Base(current)}, tail...)
		current = parent
	}
}

// Init stamps the process and binary identity and marks the stream as not yet
// established. It is called once, at daemon startup, before the first dial.
func Init(dataDir, gatewayURL, tlsMode string, serverMode bool) error {
	if dataDir == "" {
		return nil
	}
	stream := StreamConnecting
	if serverMode {
		stream = StreamListening
	}
	now := time.Now().UTC()
	exe, exeModTime := binaryIdentity()
	revision, dirty := buildIdentity()

	mu.Lock()
	defer mu.Unlock()
	return write(dataDir, State{
		PID:             os.Getpid(),
		StartedAt:       now,
		Instance:        InstanceFor(dataDir),
		Executable:      exe,
		BinaryModTime:   exeModTime,
		Revision:        revision,
		Dirty:           dirty,
		GatewayURL:      gatewayURL,
		TLSMode:         tlsMode,
		ServerMode:      serverMode,
		Stream:          stream,
		StreamChangedAt: now,
	})
}

// SetStream records a gateway-stream transition. detail is the error that
// ended the stream, if any.
func SetStream(dataDir string, s Stream, detail string) error {
	if dataDir == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()

	state, err := read(dataDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	now := time.Now().UTC()
	if state.PID == 0 {
		// The record was removed or never written (the daemon runtime is
		// embedded in another process). Stamp identity so the file is still
		// self-describing rather than a bare stream field.
		state.PID = os.Getpid()
		state.StartedAt = now
		state.Executable, state.BinaryModTime = binaryIdentity()
		state.Revision, state.Dirty = buildIdentity()
	}
	if state.Instance == "" {
		// Heal a record written before the field existed, or by a path that
		// has since become an instance dir. Derivation cannot disagree with
		// where the record sits, so this is always safe to re-apply.
		state.Instance = InstanceFor(dataDir)
	}
	if changed := state.Stream != s; changed {
		state.StreamChangedAt = now
		// Leaving an established stream is a drop, whatever it transitions to.
		// Recording it is what lets a single read distinguish "connected and
		// steady" from "connected for the fourth time this minute".
		if state.Stream.Established() {
			state.LastDisconnectAt = now
		}
		if s == StreamConnected {
			state.Sessions++
		}
	}
	state.Stream = s
	state.StreamDetail = detail
	if s == StreamConnected {
		state.ConnectedAt = now
		state.StreamDetail = ""
	}
	return write(dataDir, state)
}

// Read returns the recorded state. A missing record yields an os.IsNotExist
// error — the caller decides what "no record" means.
func Read(dataDir string) (State, error) {
	mu.Lock()
	defer mu.Unlock()
	return read(dataDir)
}

// Clear removes the record. A daemon that has exited must not leave behind a
// file claiming anything about a stream.
func Clear(dataDir string) error {
	if dataDir == "" {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	if err := os.Remove(Path(dataDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func read(dataDir string) (State, error) {
	var state State
	data, err := os.ReadFile(Path(dataDir))
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("parsing %s: %w", Path(dataDir), err)
	}
	return state, nil
}

// write replaces the record atomically so a reader never observes a half-file.
func write(dataDir string, state State) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating daemon data dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dataDir, FileName+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, Path(dataDir)); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// binaryIdentity returns the running executable's path and mtime. Both are
// best-effort: an unreadable path is reported as empty rather than failing a
// daemon boot.
func binaryIdentity() (string, time.Time) {
	exe, err := os.Executable()
	if err != nil {
		return "", time.Time{}
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	info, err := os.Stat(exe)
	if err != nil {
		return exe, time.Time{}
	}
	return exe, info.ModTime().UTC()
}

// buildIdentity returns the VCS revision the binary was built from and whether
// the tree was dirty. Go stamps these into every `go build` from a VCS
// checkout, so they cost no build-time plumbing.
func buildIdentity() (string, bool) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", false
	}
	var revision string
	var dirty bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}
	return revision, dirty
}
