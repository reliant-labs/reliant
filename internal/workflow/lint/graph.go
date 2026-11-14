// Copyright (c) 2025 Reliant Labs
package lint

import (
	"regexp"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// graph is one node graph of a definition: the workflow's own, an inline
// loop or sub-workflow body, or the on_failure handler.
type graph struct {
	wf *reliantv1.Workflow
	// prefix qualifies the graph's node ids ("", "fix_loop.", "on_failure.").
	prefix string
	// onFailure is the handler or a body inside it: its steps run once, after
	// the run has already failed, with nowhere left to route an error.
	onFailure bool
}

func graphsOf(wf *reliantv1.Workflow) []graph {
	var out []graph
	var walk func(w *reliantv1.Workflow, prefix string, onFailure bool)
	walk = func(w *reliantv1.Workflow, prefix string, onFailure bool) {
		out = append(out, graph{wf: w, prefix: prefix, onFailure: onFailure})
		for _, n := range w.GetNodes() {
			if body := inlineBody(n); body != nil {
				walk(body, prefix+n.GetId()+".", onFailure)
			}
		}
	}
	walk(wf, "", false)
	if h := wf.GetOnFailure(); h != nil {
		walk(h, "on_failure.", true)
	}
	return out
}

func inlineBody(n *reliantv1.Node) *reliantv1.Workflow {
	if la := model.GetLoopArgs(n); la != nil {
		return la.GetInline()
	}
	if swa := model.GetSubWorkflowArgs(n); swa != nil {
		return swa.GetInline()
	}
	return nil
}

// childRef is the ref a workflow or loop node runs, "" for an inline body.
func childRef(n *reliantv1.Node) string {
	if la := model.GetLoopArgs(n); la != nil {
		return model.CelStringRaw(la.GetRef())
	}
	if swa := model.GetSubWorkflowArgs(n); swa != nil {
		return model.CelStringRaw(swa.GetRef())
	}
	return ""
}

// successors maps a node id to the ids its edges (and, for a router, its
// candidates) can hand control to.
func successors(wf *reliantv1.Workflow) map[string][]string {
	out := map[string][]string{}
	for _, e := range wf.GetEdges() {
		for _, c := range e.GetCases() {
			out[e.GetFrom()] = append(out[e.GetFrom()], c.GetTo()...)
		}
		out[e.GetFrom()] = append(out[e.GetFrom()], e.GetDefault()...)
	}
	for _, n := range wf.GetNodes() {
		if r := model.GetRouterArgs(n); r != nil {
			for _, cand := range r.GetNodes() {
				out[n.GetId()] = append(out[n.GetId()], cand.GetId())
			}
		}
	}
	return out
}

// readIndex answers "does any expression in the definition read
// nodes.<id>.<field>?". It reads every string the definition holds — every
// template, condition, output, argument and command — so a read anywhere
// counts, including one inside a run command's {{ }}.
type readIndex struct {
	texts []string
	mu    sync.Mutex
	memo  map[string]bool
}

func newReadIndex(wf *reliantv1.Workflow) *readIndex {
	return &readIndex{texts: stringsOf(wf), memo: map[string]bool{}}
}

// nodeFieldPattern matches nodes.<id>.<field>, with optional-chaining `?`s
// and whitespace anywhere CEL allows them.
func nodeFieldPattern(id, field string) *regexp.Regexp {
	return regexp.MustCompile(`nodes\s*\.\s*\??\s*` + regexp.QuoteMeta(id) + `\s*\.\s*\??\s*` + regexp.QuoteMeta(field) + `\b`)
}

func (r *readIndex) reads(id, field string) bool {
	key := id + "\x00" + field
	r.mu.Lock()
	defer r.mu.Unlock()
	if hit, ok := r.memo[key]; ok {
		return hit
	}
	re := nodeFieldPattern(id, field)
	hit := false
	for _, t := range r.texts {
		if re.MatchString(t) {
			hit = true
			break
		}
	}
	r.memo[key] = hit
	return hit
}

// decodePattern finds json.decode(nodes.<id>.<field>) and parseJson(...).
var decodePattern = regexp.MustCompile(`(?:json\.decode|parseJson)\(\s*nodes\s*\.\s*\??\s*([A-Za-z_][A-Za-z0-9_-]*)\s*\.\s*\??\s*([A-Za-z_][A-Za-z0-9_]*)`)

type decodeSite struct{ node, field string }

func (r *readIndex) decodes() []decodeSite {
	var out []decodeSite
	seen := map[decodeSite]bool{}
	for _, t := range r.texts {
		for _, m := range decodePattern.FindAllStringSubmatch(t, -1) {
			s := decodeSite{node: m[1], field: m[2]}
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// stringsOf is every string value (and string map key) in a message, nested
// messages, lists and maps included — structpb values among them.
func stringsOf(m proto.Message) []string {
	var out []string
	if m == nil {
		return nil
	}
	collectStrings(m.ProtoReflect(), &out)
	return out
}

func collectStrings(m protoreflect.Message, out *[]string) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList():
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				collectValue(fd, l.Get(i), out)
			}
		case fd.IsMap():
			v.Map().Range(func(k protoreflect.MapKey, mv protoreflect.Value) bool {
				if fd.MapKey().Kind() == protoreflect.StringKind {
					*out = append(*out, k.String())
				}
				collectValue(fd.MapValue(), mv, out)
				return true
			})
		default:
			collectValue(fd, v, out)
		}
		return true
	})
}

func collectValue(fd protoreflect.FieldDescriptor, v protoreflect.Value, out *[]string) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		*out = append(*out, v.String())
	case protoreflect.MessageKind, protoreflect.GroupKind:
		collectStrings(v.Message(), out)
	}
}
