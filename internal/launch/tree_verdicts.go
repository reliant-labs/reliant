// Copyright (c) 2025 Reliant Labs
package launch

import (
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"sync"

	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
)

// treeVerdictCacheSize bounds the memoized verdicts: one per distinct
// (workflow definition, scope), so a few per activated workflow.
const treeVerdictCacheSize = 1024

// treeVerdicts memoizes validateResolvedWorkflowTree. Static analysis of a
// workflow tree is a pure function of the root's definition and of what its
// loaders return (the children and presets it reaches), and it is expensive:
// it compiles and type-checks every CEL expression in the tree. ListInbox
// resolves every enabled activation's workflow on every read, and for one
// user with twenty activations over a 420 KB catalog that analysis was ~3s of
// each read in prod (2026-10-31).
//
// An entry is keyed by the root's content and the scope, and records a
// digest of everything the analysis loaded. It is reused only after every one
// of those loads is repeated and returns the same content (holds), so a
// changed child, a preset edit or a re-sync that moves a ref to another file
// is a miss. The loads are resolutions, not analysis: cheap.
var treeVerdicts = &treeVerdictCache{items: make(map[[sha256.Size]byte]*treeVerdict, treeVerdictCacheSize)}

// analyzeTree is the analysis treeVerdicts saves; a test counts its runs.
var analyzeTree = validation.StaticAnalysisWithOptions

type treeVerdictCache struct {
	mu    sync.Mutex
	items map[[sha256.Size]byte]*treeVerdict
	order [][sha256.Size]byte // insertion order, oldest first
}

func (c *treeVerdictCache) get(key [sha256.Size]byte) (*treeVerdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

func (c *treeVerdictCache) add(key [sha256.Size]byte, v *treeVerdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[key]; !ok {
		c.order = append(c.order, key)
	}
	c.items[key] = v
	for len(c.order) > treeVerdictCacheSize {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

// treeVerdictKey identifies one analysis: the root's definition, the name it
// is validated as (it appears in the verdict and decides which refs are the
// root itself) and every scope field resolution reads. keyed is false when
// the root cannot be serialized, which leaves it unmemoized.
func treeVerdictKey(scope Scope, workflowName string, wf *reliantv1.Workflow) (key [sha256.Size]byte, keyed bool) {
	root, err := proto.MarshalOptions{Deterministic: true}.Marshal(wf)
	if err != nil {
		return key, false
	}
	h := sha256.New()
	for _, s := range []string{
		workflowName, scope.UserID, scope.ProjectID, scope.DaemonID, scope.CatalogPin, scope.draftRoot,
		strconv.FormatBool(scope.ProjectFirst), strconv.FormatBool(scope.draftOptional),
	} {
		h.Write([]byte(strconv.Itoa(len(s))))
		h.Write([]byte{':'})
		h.Write([]byte(s))
	}
	h.Write(root)
	copy(key[:], h.Sum(nil))
	return key, true
}

// treeVerdict is one analysis's outcome and what it depended on.
type treeVerdict struct {
	// children and presets map each ref and preset name the analysis loaded
	// to a digest of what the load returned (its definition or its error).
	children, presets map[string][sha256.Size]byte
	// consistent is false when one ref or preset loaded two different ways
	// within the analysis (a sync landed mid-way): its verdict is not kept.
	consistent bool
	// reason is the failure's reason; empty when the tree is valid.
	reason string
}

func newTreeVerdict() *treeVerdict {
	return &treeVerdict{children: map[string][sha256.Size]byte{}, presets: map[string][sha256.Size]byte{}, consistent: true}
}

// err is the verdict as validateResolvedWorkflowTree returns it: a fresh
// error per call, so no caller shares one.
func (v *treeVerdict) err() error {
	if v.reason == "" {
		return nil
	}
	return &ValidationError{Kind: ValidationFailedPrecondition, Reason: v.reason}
}

func (v *treeVerdict) recordChild(ref string, wf *reliantv1.Workflow, err error) {
	digest, ok := workflowDigest(wf, err)
	v.record(v.children, ref, digest, ok)
}

func (v *treeVerdict) recordPreset(name string, params map[string]interface{}, err error) {
	digest, ok := presetDigest(params, err)
	v.record(v.presets, name, digest, ok)
}

// record notes one load. A load that cannot be digested, or that returned
// something else earlier in the same analysis, leaves the verdict unkept.
func (v *treeVerdict) record(m map[string][sha256.Size]byte, name string, digest [sha256.Size]byte, ok bool) {
	if prev, seen := m[name]; !ok || (seen && prev != digest) {
		v.consistent = false
	}
	m[name] = digest
}

// holds repeats every load the analysis made and reports whether each still
// returns what it did then.
func (v *treeVerdict) holds(loadChild func(string) (*reliantv1.Workflow, error), loadPreset func(string) (map[string]interface{}, error)) bool {
	for ref, want := range v.children {
		if got, ok := workflowDigest(loadChild(ref)); !ok || got != want {
			return false
		}
	}
	for name, want := range v.presets {
		if loadPreset == nil {
			return false
		}
		if got, ok := presetDigest(loadPreset(name)); !ok || got != want {
			return false
		}
	}
	return true
}

// workflowDigest digests what a child load returned: its definition, or its
// error. ok is false when the definition cannot be serialized.
func workflowDigest(wf *reliantv1.Workflow, err error) (digest [sha256.Size]byte, ok bool) {
	if err != nil {
		return sha256.Sum256([]byte("err:" + err.Error())), true
	}
	b, merr := proto.MarshalOptions{Deterministic: true}.Marshal(wf)
	if merr != nil {
		return digest, false
	}
	return sha256.Sum256(append([]byte("wf:"), b...)), true
}

// presetDigest is workflowDigest for a preset load.
func presetDigest(params map[string]interface{}, err error) (digest [sha256.Size]byte, ok bool) {
	if err != nil {
		return sha256.Sum256([]byte("err:" + err.Error())), true
	}
	b, merr := json.Marshal(params) // sorts map keys: deterministic
	if merr != nil {
		return digest, false
	}
	return sha256.Sum256(append([]byte("preset:"), b...)), true
}
