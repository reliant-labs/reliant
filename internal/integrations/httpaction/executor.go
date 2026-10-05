package httpaction

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// ExecutorCall is what a Go executor receives: the action's params, already
// validated against the manifest schema with defaults applied, and the
// resolved credential (nil when the integration takes none and the caller
// named none).
//
// The credential's plaintext stays behind Apply. An executor that builds its
// own requests must apply it only to the integration's hosts and must not
// return it; the runner scrubs whatever comes back regardless.
type ExecutorCall struct {
	Manifest   *reliantv1.IntegrationManifest
	Action     *reliantv1.ActionSpec
	Params     map[string]any
	Credential Credential
	// Runner is the guarded runner, for executors that make HTTP calls: its
	// Client goes through the SSRF guard and never follows a redirect off the
	// integration's hosts.
	Runner *Runner
}

// ExecutorFunc implements an action whose manifest says `executor: go:<name>`.
// It honours the same contract as a declarative action: a failed call is a
// Result with IsError, and a Go error means the call could not be attempted.
type ExecutorFunc func(ctx context.Context, call ExecutorCall) (*Result, error)

// ExecutorRegistry maps go:<name> to Go functions. Register at process start;
// lookups are concurrent-safe.
type ExecutorRegistry struct {
	mu    sync.RWMutex
	funcs map[string]ExecutorFunc
}

// NewExecutorRegistry returns an empty registry.
func NewExecutorRegistry() *ExecutorRegistry {
	return &ExecutorRegistry{funcs: map[string]ExecutorFunc{}}
}

// defaultExecutors is the process registry every Runner dispatches through
// unless given another. Provider packages register into it from init (as
// database/sql drivers do), so the action node and the agent tool path see
// the same executors without separate wiring.
var defaultExecutors = NewExecutorRegistry()

// Executors returns the process-wide executor registry.
func Executors() *ExecutorRegistry { return defaultExecutors }

// Register adds an executor. Registering a name twice, or a nil function, is
// an error: two packages claiming one name is a wiring defect.
func (r *ExecutorRegistry) Register(name string, fn ExecutorFunc) error {
	if fn == nil {
		return fmt.Errorf("executor %q: nil function", name)
	}
	if _, ok := manifest.ExecutorName(&reliantv1.ActionSpec{Executor: manifest.ExecutorPrefix + name}); !ok {
		return fmt.Errorf("executor name %q is not valid", name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.funcs[name]; dup {
		return fmt.Errorf("executor %q is already registered", name)
	}
	r.funcs[name] = fn
	return nil
}

// MustRegister is Register for init-time wiring.
func (r *ExecutorRegistry) MustRegister(name string, fn ExecutorFunc) {
	if err := r.Register(name, fn); err != nil {
		panic(err)
	}
}

func (r *ExecutorRegistry) lookup(name string) (ExecutorFunc, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn, ok := r.funcs[name]
	return fn, ok
}

// CheckManifests reports every go: executor the manifests name that is not
// registered, so a process can fail at boot instead of at first call.
func (r *ExecutorRegistry) CheckManifests(ms []*reliantv1.IntegrationManifest) error {
	var missing []string
	for _, m := range ms {
		for _, a := range m.GetActions() {
			name, ok := manifest.ExecutorName(a)
			if !ok {
				continue
			}
			if _, found := r.lookup(name); !found {
				missing = append(missing, fmt.Sprintf("%s/%s (%s)", m.GetId(), a.GetId(), name))
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("integration actions name unregistered executors: %s", strings.Join(missing, ", "))
	}
	return nil
}

// WithExecutors returns a copy of the runner that dispatches go: actions to reg.
func (r *Runner) WithExecutors(reg *ExecutorRegistry) *Runner {
	clone := *r
	clone.executors = reg
	return &clone
}

// Client is the guarded HTTP client, for executors.
func (r *Runner) Client() *http.Client { return r.client }

func (r *Runner) runExecutor(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, name string, params map[string]any, cred Credential) (*Result, error) {
	fn, ok := r.executors.lookup(name)
	if !ok {
		return nil, fmt.Errorf("%s/%s: executor %q is not registered in this process", m.GetId(), a.GetId(), name)
	}
	res, err := fn(ctx, ExecutorCall{Manifest: m, Action: a, Params: params, Credential: cred, Runner: r})
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, fmt.Errorf("%s/%s: executor %q returned no result", m.GetId(), a.GetId(), name)
	}
	if res.Content == "" && res.Data != nil {
		content, _ := json.Marshal(res.Data)
		res.Content = truncate(string(content))
	}
	return res, nil
}
