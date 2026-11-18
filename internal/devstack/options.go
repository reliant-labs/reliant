package devstack

import (
	"strconv"
	"sync"
)

// Options are the raw git facts pushed INTO KCL as options for this forge
// command. The zero value (both fields "") is the default stack — it emits
// NO -D args, so a plain render sees option("worktree") == None (KCL
// default "") and option("branch") == None, rendering byte-identically to
// a stack with no dev-stack parameterization.
type Options struct {
	Worktree string // option("worktree"): linked-worktree basename, "" on primary
	Branch   string // option("branch"): sanitized current branch, always
}

// Resolve gathers the git facts for projectDir. Cheap and side-effect-free
// (no files, no lock): the durable state lives in the block registry, which
// allocate_port touches lazily. Outside a repo both facts are "".
func Resolve(projectDir string) Options {
	return Options{
		Worktree: Worktree(projectDir),
		Branch:   Branch(projectDir),
	}
}

// DArgs returns the `-D key=value` KCL option bindings that push these git
// facts into the render — the extension of the existing namespace/image_tag
// option seam. An empty fact is OMITTED (not emitted as ""), so the default
// stack returns NO args and renders byte-identically. Values are QUOTED KCL
// string literals so an all-digit worktree/branch name stays str, never an
// int (the same coercion fix as image_tag).
func (o Options) DArgs() []string {
	var args []string
	if o.Worktree != "" {
		args = append(args, "worktree="+strconv.Quote(o.Worktree))
	}
	if o.Branch != "" {
		args = append(args, "branch="+strconv.Quote(o.Branch))
	}
	return args
}

// The active options are a process-global, set ONCE at the start of a forge
// command (forge env up / forge env deploy) and read by every render path — both
// the entity render (renderKCLRaw) and the manifest render
// (cluster.renderDArgs). A global is how forge already threads per-command
// render context (cf. the port store) without churning ~20 render call
// sites.
//
// The zero value emits no args, so any command that never calls SetActive
// (forge ci, forge project audit, tests) renders byte-identically to today.
var (
	activeMu sync.RWMutex
	active   Options
)

// SetActive records the git facts for this process's subsequent renders.
// Call once, before the first render, on the up/deploy path only.
func SetActive(o Options) {
	activeMu.Lock()
	active = o
	activeMu.Unlock()
}

// Active returns the options set by SetActive (zero value when unset).
func Active() Options {
	activeMu.RLock()
	defer activeMu.RUnlock()
	return active
}

// ActiveDArgs is the convenience the render paths call: the `-D` bindings
// to push the active git facts into KCL (nil for the default stack).
func ActiveDArgs() []string { return Active().DArgs() }
