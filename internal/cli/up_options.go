package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/internal/kcloptions"
)

// The `-D name=value` render options a `forge env up` was invoked with.
//
// forge relays these to KCL and does NOT interpret them. It never parses a
// value, never knows what an option means, and never acts on one itself —
// the env's KCL reads `option("<name>")` and decides. That is the whole
// contract: forge owns the transport, the project owns the meaning.
//
// Only `forge env up` binds them. `forge env deploy` deliberately does not:
// a caller-supplied option that reshapes a cluster render is how a prod apply
// stops being reproducible from the repo alone.
//
// Threaded as a process-global set once before the first render — the same
// shape internal/devstack uses for the git facts, and for the same reason:
// ~20 render call sites, one per-command fact. The zero value emits nothing,
// so every command that never sets it renders byte-identically.
var (
	renderOptsMu sync.RWMutex
	renderOpts   []string // "name=<quoted value>" bindings, render-ready
)

// setRenderOptions records the bindings for this process's subsequent renders.
func setRenderOptions(dArgs []string) {
	renderOptsMu.Lock()
	renderOpts = dArgs
	renderOptsMu.Unlock()
}

// activeRenderOptionDArgs is what the render paths call. nil when no -D was
// passed.
func activeRenderOptionDArgs() []string {
	renderOptsMu.RLock()
	defer renderOptsMu.RUnlock()
	return renderOpts
}

// parseRenderOptions turns raw `-D name=value` flag values into KCL bindings.
//
// The value is taken verbatim and wrapped in a KCL string literal. Quoting is
// not cosmetic: an unquoted binding is parsed as a KCL literal, so `-D
// port=8080` would arrive as an int and `-D tags={"a":"b"}` as a dict. Forcing
// str keeps forge out of the business of guessing the type — the KCL author
// declared it and can convert. (Same coercion fix image_tag and the devstack
// git facts already carry.)
func parseRenderOptions(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]string, len(values))
	names := make([]string, 0, len(values))
	for _, raw := range values {
		idx := strings.Index(raw, "=")
		if idx < 0 {
			return nil, fmt.Errorf("-D %s: expected name=value", raw)
		}
		name := strings.TrimSpace(raw[:idx])
		value := raw[idx+1:] // verbatim: spaces may be meaningful to the project
		if name == "" {
			return nil, fmt.Errorf("-D %s: missing option name before '='", raw)
		}
		if why, reserved := kcloptions.Reserved[name]; reserved {
			return nil, fmt.Errorf(
				"-D %s: %q is set by forge, not by you — %s", raw, name, why)
		}
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("-D: %q given twice (%q then %q); pick one", name, prev, value)
		}
		seen[name] = value
		names = append(names, name)
	}

	sort.Strings(names) // deterministic bindings → byte-identical repeat renders
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, name+"="+strconv.Quote(seen[name]))
	}
	return out, nil
}

// validateRenderOptions checks the -D names against the options the env's KCL
// actually declares, so a typo fails before the render instead of silently
// doing nothing (an unread option is not an error in KCL — it is just unread).
//
// Degrades PERMISSIVELY: when discovery can't see the project's options at all
// it returns nil rather than rejecting. A caller who passed a real option must
// not be blocked because forge failed to parse the KCL — the render is about to
// happen anyway and will report any genuine problem with a better message.
func validateRenderOptions(projectDir, envName string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	declared, discoverable, err := kcloptions.Discover(projectDir, envName)
	if err != nil || !discoverable {
		return nil
	}
	known := make(map[string]bool, len(declared))
	for _, o := range declared {
		known[o.Name] = true
	}

	var unknown []string
	for _, raw := range values {
		name := strings.TrimSpace(raw)
		if idx := strings.Index(raw, "="); idx >= 0 {
			name = strings.TrimSpace(raw[:idx])
		}
		if name != "" && !known[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}

	sort.Strings(unknown)
	msg := fmt.Sprintf("-D: deploy/kcl/%s/ declares no option named %s",
		envName, quoteJoin(unknown))
	if len(declared) == 0 {
		return fmt.Errorf("%s (it declares none; add `option(\"<name>\")` to its KCL)", msg)
	}
	names := make([]string, 0, len(declared))
	for _, o := range declared {
		names = append(names, o.Name)
	}
	return fmt.Errorf("%s; it declares %s (see `forge env options %s`)",
		msg, quoteJoin(names), envName)
}

func quoteJoin(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = strconv.Quote(s)
	}
	return strings.Join(quoted, ", ")
}
