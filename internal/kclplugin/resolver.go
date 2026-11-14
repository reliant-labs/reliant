// Package kclplugin registers forge's in-process KCL plugin namespace
// (kcl_plugin.forge.*), letting KCL pull host-runtime values during
// evaluation instead of forge having to pre-enumerate and inject them.
//
// It provides two port primitives, plus derive_jwk:
//
//   - resolve_port(name, preferred) — a pure-Go free-port allocator. KCL
//     declares `port = forge.resolve_port("reliant-web", 3000)` inline, by
//     any name it likes, and binds it to a variable other declarations
//     reference (the frontend's port, env-var URLs, CORS origins). One
//     declaration, referenced everywhere — forge owns the allocation, KCL
//     owns the plumbing. The allocation is AVAILABILITY-CHECKED (it steps
//     off busy ports), so it suits ports that may float.
//
//   - allocate_port(base, key) — a DETERMINISTIC, memoized keyed allocator
//     for parallel dev stacks: returns base + block(key)*100, where forge
//     assigns a stable small block per key (the index is internal, never
//     surfaced in KCL) and persists it under a file lock so up and deploy
//     agree. key "" ⇒ base unchanged. Unlike resolve_port it does NOT step
//     off busy ports — it must match externally-fixed ports (a k3d
//     pre-mapped host port; the host reliant's listen port). The block
//     engine lives in internal/devstack; this package holds a settable hook
//     (UseBlockAllocator) so the CLI wires the lock-guarded registry in.
//
// WHAT USED TO LIVE HERE. An earlier resolve_oidc_client primitive applied
// resolve_port's shape to an OIDC client_id — a value the IdP generates at
// registration and that therefore cannot be declared up front, resolved
// from INSIDE a render by dialing the IdP mid-evaluation. That solved a
// problem that does not exist: a render never actually needs to LEARN a
// generated value, because a workload can reference it BY NAME instead. The
// generated identity is now converged by `forge env up` (pkg/identity, the
// library the control plane runs too), which publishes it to the env's
// secret store; every render lowers forge.IdentityRef to those keys. See
// the `auth/dev-loop` skill.
//
// The plugin bridge that lets KCL call back into Go is forge's own purego
// callback (bridge.go) — no cgo, so every forge binary on every OS has it.
package kclplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/devstack"
)

// PortResolver hands out host ports, one per logical name, stable for the
// life of the resolver: repeated Resolve("x", …) calls — including across
// the several KCL renders a single forge command performs — return the
// same port, so a component's bound port and the URLs composed from it
// never drift.
//
// Allocation order for a never-seen name:
//  1. the port it got on a previous run (when a store is configured and
//     that port is still free) — stable dev ports across runs;
//  2. the requested `preferred` port, then a short scan upward from it
//     (3000 → 3001 → …) so dev ports stay human-friendly and parity-stable;
//  3. an OS-assigned free port as the last resort.
//
// It never hands the same port to two names. Availability means BOTH
// bindable and unanswered — see portFree, where a docker-published port
// that a bind probe alone reports free is the case that forced the second
// check. Like any probe-then-bind scheme (cf. cloud-dev.sh's _pick_port)
// there is an inherent TOCTOU window between resolving a port and the
// launched process binding it; acceptable for the dev loop this serves.
type PortResolver struct {
	mu        sync.Mutex
	byName    map[string]int // confirmed this run
	claimed   map[int]bool
	persisted map[string]int // last run's assignments (tentative; reused if still free)
	storePath string         // when set, byName is saved here for cross-run reuse
	readOnly  bool           // read the store, never write it (read-only renders)
	bound     bool           // a store path has been armed (read-only or writable)
	available func(int) bool // nil uses the real bind+dial probe; tests inject a pure probe
	scope     string         // exact checkout + env identity in the shared store
}

// Bound reports whether this resolver has been pointed at a store file. The
// render seam uses it to arm the READ half by default without clobbering a
// writable store a launch path already armed.
func (r *PortResolver) Bound() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bound
}

// scanWindow bounds the upward search from the preferred port before
// falling back to an OS-assigned port.
const scanWindow = 64

// NewPortResolver returns an in-memory PortResolver that allocates ports
// for the current run only, without persisting assignments across runs.
func NewPortResolver() *PortResolver {
	return &PortResolver{byName: map[string]int{}, claimed: map[int]bool{}, persisted: map[string]int{}}
}

// NewPersistentPortResolver remembers assignments in a JSON file at path,
// so a name reuses the same port across forge runs. Best-effort: a
// read/write error never fails a render — it degrades to fresh allocation.
func NewPersistentPortResolver(path string) *PortResolver {
	r := NewPortResolver()
	r.storePath = path
	r.bound = true
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &r.persisted)
	}
	for _, port := range r.persisted {
		r.claimed[port] = true
	}
	return r
}

// NewReadOnlyPortResolver READS an existing store but never writes one. It is
// what every render that is not a launch gets, and it is the half that was
// missing: the store's whole purpose is that a port allocated once stays put,
// and a reporting command that re-probed from scratch reported a DIFFERENT
// port than the stack it was describing is actually on.
//
// It does not write, because a read-only command must not create machine-local
// state — and more importantly because the first resolution has to happen while
// the port is still free to be meaningful. `forge env config` run against a
// RUNNING stack would otherwise record the port the live service just made
// forge step off, pinning the wrong answer permanently.
func NewReadOnlyPortResolver(path string) *PortResolver {
	r := NewPersistentPortResolver(path)
	r.readOnly = true
	return r
}

// Resolve returns the port for name (see allocation order on PortResolver).
func (r *PortResolver) Resolve(name string, preferred int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.scope != "" {
		name = r.scope + ":" + name
	}
	if r.storePath != "" && !r.readOnly {
		var port int
		err := devstack.WithFileLock(r.storePath+".lock", func() error {
			// Reload INSIDE the cross-process transaction, including roles this
			// process has not resolved. Persist the union, never just this run.
			data, err := os.ReadFile(r.storePath)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			r.persisted = map[string]int{}
			if err == nil {
				if err := json.Unmarshal(data, &r.persisted); err != nil {
					return err
				}
			}
			r.claimed = map[int]bool{}
			for _, p := range r.persisted {
				r.claimed[p] = true
			}
			if p, ok := r.persisted[name]; ok {
				port = p
				return nil
			}
			port, err = r.resolve(name, preferred)
			if err != nil {
				return err
			}
			r.persisted[name] = port
			return writePorts(r.storePath, r.persisted)
		})
		return port, err
	}
	return r.resolve(name, preferred)
}

func (r *PortResolver) resolve(name string, preferred int) (int, error) {
	if p, ok := r.byName[name]; ok {
		return p, nil
	}
	// 1. Reuse last run's port for this name. UNCONDITIONALLY — a port
	//    already assigned to a name is that name's port, and an
	//    availability check here would defeat the store's entire purpose.
	//
	//    The check that used to be here re-probed the port and moved on
	//    when it was busy, which produced exactly the wrong answer in the
	//    most ordinary situation there is: the thing that OWNS this port is
	//    still running from the last `forge env up`. It answers, so the probe
	//    called it busy, so the render handed out a different port — and
	//    the stack came up beside its own still-running database instead of
	//    reusing it, with the app now dialling a port nothing was on.
	//
	//    If a FOREIGN process has taken the port in the meantime, the
	//    consumer discovers it at bind time and says so — a loud, specific
	//    failure naming the port and the declaration that owns it (see
	//    internal/hostinfra's holder check) — rather than a silent drift
	//    into a different port whose consequences surface somewhere else.
	//    A stable port that occasionally collides is a better contract than
	//    an unstable one that never does.
	if p, ok := r.persisted[name]; ok && p > 0 {
		return r.assign(name, p), nil
	}
	// 2. Prefer the requested port, then scan upward from it.
	if preferred > 0 {
		for off := 0; off < scanWindow; off++ {
			cand := preferred + off
			if cand > 65535 {
				break
			}
			if !r.claimed[cand] && r.portFree(cand) {
				return r.assign(name, cand), nil
			}
		}
	}
	// 3. OS-assigned free port.
	for i := 0; i < 100; i++ {
		p, err := freePort()
		if err != nil {
			return 0, err
		}
		if !r.claimed[p] {
			return r.assign(name, p), nil
		}
	}
	return 0, fmt.Errorf("resolve_port(%q): exhausted free-port search", name)
}

// writePorts commits the shared reservation map atomically. Read-only renders
// do not take the writer lock and must never see a truncated JSON file.
//
// Through atomicfile, not a bare CreateTemp+Rename: on Windows a rename onto
// a file another process (or goroutine) has open for reading fails with
// ERROR_ACCESS_DENIED, and read-only renders read the store without the
// lock, so the writer lost its reservation whenever a reader overlapped it
// (TestPersistentResolversReserveAcrossProcessesAndRoles on Windows CI).
// atomicfile.WriteFile retries that transient refusal.
func writePorts(path string, ports map[string]int) error {
	data, err := json.MarshalIndent(ports, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(path, data, 0o600)
}

// assign records an allocation in this resolver; Resolve commits the union.
func (r *PortResolver) assign(name string, port int) int {
	r.byName[name] = port
	r.claimed[port] = true
	return port
}

func (r *PortResolver) portFree(port int) bool {
	if r.available != nil {
		return r.available(port)
	}
	return portFree(port)
}

// portFree reports whether a port can be handed to a process that will
// BIND it and to a client that will then DIAL it — which takes two probes,
// not one, because on a developer machine those can disagree.
//
// A bind succeeding is not sufficient. Docker's userland proxy publishes a
// container port by listening on the wildcard address (`*:5433`), and macOS
// lets a subsequent bind to `localhost:5433` succeed anyway — different
// address, no conflict, as far as the kernel is concerned. So a bind-only
// probe reports FREE for a port that another stack's database is actively
// answering on. The port then gets handed out, and whatever binds it either
// loses the race for new connections or leaves clients reaching the other
// listener: a dev stack silently talking to a foreign database, which is
// the exact failure the port declaration exists to prevent.
//
// A dial succeeding is not sufficient either, in the other direction: it
// proves someone is listening but says nothing about whether WE could bind.
// Requiring both is what makes "free" mean usable.
//
// A dial that hangs is treated as occupied — a port that takes longer than
// the timeout to answer is not one to hand a dev server. The timeout is
// short because this runs once per port considered, inside a render.
func portFree(p int) bool {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("localhost:%d", p))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return !portAnswering(p)
}

// portAnswering reports whether anything accepts a connection on the port,
// across BOTH loopback families. Checking both matters: docker publishes on
// the wildcard address, so a container can be reachable over ::1 while
// 127.0.0.1 looks quiet (or the reverse), and either one is a listener a
// client resolving "localhost" may land on.
func portAnswering(p int) bool {
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, p), portProbeTimeout)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// portProbeTimeout bounds each dial in the availability probe. Loopback
// either answers immediately or does not answer at all, so this only has to
// out-wait a busy machine, not a network.
const portProbeTimeout = 250 * time.Millisecond

func freePort() (int, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// defaultResolver backs the kcl_plugin.forge.resolve_port method. Process-
// global so ports stay stable across the renders one forge command runs.
//
// Why this is a synchronised global and not per-render state: resolve_port is
// invoked by the KCL runtime through the purego plugin bridge, which carries
// only the method name and its arguments — there is no context, options or
// resolver instance to thread through it. And the evaluation lock
// (Serialized) cannot own it either: arming happens BEFORE a render, from
// several goroutines (`forge env status` and topology render every env
// concurrently), while an evaluation is in flight on another. So the one
// slot is guarded by resolverMu; every read goes through currentResolver and
// every swap through armResolver / UsePortStoreReadOnly, never a bare
// assignment.
var (
	resolverMu      sync.Mutex
	defaultResolver = NewPortResolver()
)

// currentResolver returns the resolver resolve_port should use right now.
func currentResolver() *PortResolver {
	resolverMu.Lock()
	defer resolverMu.Unlock()
	return defaultResolver
}

// armResolver installs r as the process's resolver.
func armResolver(r *PortResolver) {
	resolverMu.Lock()
	defaultResolver = r
	resolverMu.Unlock()
}

// blockAllocator backs the kcl_plugin.forge.allocate_port method: given
// (base, key) it returns the deterministic, memoized port base+block*100.
// The real implementation (internal/devstack.AllocatePort under a file
// lock) is injected via UseBlockAllocator on the up/deploy path — keeping
// this package free of an import cycle / filesystem concern. The default
// (no allocator armed) returns base unchanged for ANY key, so a read-only
// render like `forge ci` resolves allocate_port without persisting state.
//
// Process-global + mutex-guarded so it is safe to swap once per command and
// read from KCL's evaluation goroutines.
var (
	blockAllocMu sync.Mutex
	blockAlloc   func(base int, key string) (int, error)
)

// UseBlockAllocator arms allocate_port with fn for this process. Call once
// before rendering, on the up/deploy path. Both `forge env up` AND `forge
// deploy` arm the SAME (lock-guarded, persistent) allocator, so the two
// commands resolve identical ports for a given key — the up-vs-deploy fix.
func UseBlockAllocator(fn func(base int, key string) (int, error)) {
	blockAllocMu.Lock()
	blockAlloc = fn
	blockAllocMu.Unlock()
}

// allocatePort is the body the plugin calls. Falls back to base unchanged
// (block 0) when no allocator is armed.
func allocatePort(base int, key string) (int, error) {
	blockAllocMu.Lock()
	fn := blockAlloc
	blockAllocMu.Unlock()
	if fn == nil {
		return base, nil
	}
	port, err := fn(base, key)
	if err != nil {
		return 0, err
	}
	if err := currentResolver().reserveFixed(base, key, port); err != nil {
		return 0, err
	}
	return port, nil
}

// devStacksFn backs the kcl_plugin.forge.dev_stacks method: the roster of
// registered dev-stack keys (git worktrees), for a KCL module that generates
// one config block PER RUNNING STACK — a NATS account, a DB name, a vhost.
//
// This exists so such a generator never has to read .forge/blocks.json
// itself. When control-plane's dev NATS generator did exactly that, it had no
// way to tell a worktree key from an ordinary port-block key, so prod's
// reliant-web port key rendered a bogus `CP_prod` NATS account into a TRACKED
// config file. The registry cannot answer "which keys are stacks?" from
// outside; forge knows, so forge should be the one to say.
//
// Armed on the up/deploy path only, exactly like blockAlloc.
var (
	devStacksMu sync.Mutex
	devStacksFn func() ([]string, error)
)

// UseDevStacks arms dev_stacks with fn for this process.
func UseDevStacks(fn func() ([]string, error)) {
	devStacksMu.Lock()
	devStacksFn = fn
	devStacksMu.Unlock()
}

// devStacks is the body the plugin calls. Unarmed it returns EMPTY, which is
// the same contract allocate_port has when unarmed (base unchanged): a
// read-only render resolves the builtin without touching machine-local state.
//
// That default is load-bearing, not a convenience. `forge generate` renders
// KCL only to READ from it, but evaluating a module fires every file.write in
// it — so a generator keyed on this builtin would otherwise emit whatever
// worktrees happened to exist on THIS machine into a tracked file, and two
// developers on one commit would generate different bytes. Returning empty
// makes the generate-path render a pure function of committed inputs: it emits
// the default stack's config and nothing else, on every machine. This is the
// root fix for the incident internal/cli/kcl_render_purity.go guards against.
func devStacks() ([]string, error) {
	devStacksMu.Lock()
	fn := devStacksFn
	devStacksMu.Unlock()
	if fn == nil {
		return []string{}, nil
	}
	return fn()
}

// ResetDefaultResolverForTest returns the process-global resolver to its
// unbound, in-memory default. Tests only: the resolver is process-global so
// ports stay stable across the several renders one command performs, which
// means one test's arming would otherwise leak into the next.
func ResetDefaultResolverForTest() {
	armResolver(NewPortResolver())
}

// UsePortStoreReadOnly points the resolver at path for READING only, unless a
// writable store is already armed — in which case it does nothing, so a launch
// path that armed the real store keeps it.
//
// This is what closes the gap between what the scaffolded KCL documents and
// what forge did. `resolve_port` is documented as remembering its answer in
// .forge/ports-<env>.json "so it is stable from then on", and the writable
// store delivered exactly that — on `env up` and `env deploy`, the only two
// paths that armed it. Every OTHER render (env config, env render, db reset's
// DSN reconciliation, status, doctor, build) ran with an UNBOUND resolver, so
// it ignored the recorded answer and re-probed from zero.
//
// That is worse than not persisting at all, because the re-probe's result
// depends on whether the stack is running: the live postgres ANSWERS on its
// port, portFree says busy, and forge hands out the next port — stepping off
// the database it was asked to describe. `forge env config` then printed one
// DSN while `forge env up` had launched on another, and `forge db reset` was
// handed a port nothing listens on.
//
// Reading is always safe and always correct: the stored value IS the answer a
// previous resolution already committed to.
func UsePortStoreReadOnly(path string) {
	// Check and swap under one lock: two concurrent renders must not both see
	// an unbound resolver and then overwrite each other's (or a launch path's
	// writable) store.
	resolverMu.Lock()
	defer resolverMu.Unlock()
	if defaultResolver.Bound() {
		return
	}
	defaultResolver = NewReadOnlyPortResolver(path)
}

// UsePortStore swaps the global resolver for one that persists assignments
// to path (cross-run port stability), making that file the SINGLE SOURCE
// OF TRUTH for resolve_port: once allocated (availability-checked), a
// (role) -> port mapping is read back identically on every subsequent
// render. Both `forge env up` AND `forge env deploy` call this with the same
// instance-scoped path, so the two commands resolve identical ports — the
// fix for the up-vs-deploy port drift.
//
// Call once before rendering, only on the dev-launch / deploy path — not
// for read-only renders like `forge ci`, which shouldn't write a ports
// file. Safe to call repeatedly.
//
// It returns a restore func that reverts the store file to its bytes at
// call time (or removes it if it didn't exist). A render shifts + persists
// a port when the preferred one is busy; a caller whose render is then
// REJECTED (e.g. up's already-running guard) calls restore so the rejected
// attempt can't drift the stable assignments. Callers that commit the
// render ignore it.
func UsePortStore(path string) (restore func()) {
	snapshot, readErr := os.ReadFile(path)
	existed := readErr == nil
	armResolver(NewPersistentPortResolver(path))
	return func() {
		if existed {
			_ = atomicfile.WriteFile(path, snapshot, 0o644)
		} else {
			_ = os.Remove(path)
		}
	}
}
