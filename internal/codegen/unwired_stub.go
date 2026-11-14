// File: internal/codegen/unwired_stub.go
//
// The `forge:gen unwired-stub` marker: stamped on every handler method
// forge emits as a PLACEHOLDER for a proto RPC nobody has implemented
// yet. The marker — not the method's body, not its filename — is how a
// reader tells "forge wrote this and it is still pending work" from
// "somebody implemented this".
//
// Every path that emits such a placeholder must stamp it. Today that is
// the generate pipeline's handler stub templates
// (templates/service/handlers*.go.tmpl, via GenerateMissingHandlerStubs)
// and `forge scaffold rpc` (internal/cli/scaffold/rpc.go, for an RPC not
// yet in the proto). unwired_stub_emitters_test.go derives that set from
// the stub body they share rather than naming them, so a new emitter
// that forgets the marker fails instead of drifting.
//
// This file is the marker's single source of truth, shared by its
// readers:
//
//   - CRUD gen's stub→shim excision (unwired_stub_excise.go), which
//     removes a PRISTINE marked stub when its RPC becomes entity-backed,
//   - `forge project audit` (orphan-stub category),
//   - out-of-tree orchestrators, which grep the marker to build the list
//     of handlers still awaiting an implementation.
//
// It lives in codegen — not internal/scaffold — because the marker is a
// property of the GENERATED stub surface; the scaffolder merely stamps
// the same thing codegen defines.

package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// UnwiredStubMarkerPrefix is the literal that opens the marker comment.
// Every emitter builds its marker from UnwiredStubMarkerComment (which is
// built from this), and UnwiredStubMarkerRE reads it back, so the marker
// text exists in exactly one place.
const UnwiredStubMarkerPrefix = "forge:gen unwired-stub symbol="

// UnwiredStubMarkerComment returns the whole marker doc-comment line for
// one handler method — `// forge:gen unwired-stub symbol=<pkg>.<Method>`.
// It goes LAST in the method's doc comment: revive's `exported` rule
// requires the FIRST line to start with the method name, so the marker
// cannot lead.
func UnwiredStubMarkerComment(servicePkg, method string) string {
	return "// " + UnwiredStubMarkerPrefix + servicePkg + "." + method
}

// UnwiredStubMarkerRE matches the marker line
// `// forge:gen unwired-stub symbol=<pkg>.<Method>` that every unwired-stub
// emitter stamps, capturing the symbol. It is the single source of truth
// shared by the excision pass (unwired_stub_excise.go) and
// `forge project audit` (orphan-stub category).
var UnwiredStubMarkerRE = regexp.MustCompile(`//\s*` + regexp.QuoteMeta(UnwiredStubMarkerPrefix) + `(\S+)`)

// ScanUnwiredStubMethods returns the set of RPC method names in one
// handler directory that still carry the unwired-stub marker — i.e. the
// generated CodeUnimplemented stubs nobody has implemented yet. Non-test,
// non-_gen.go files only (the marker only ever lands in the user-owned
// handler files). Unreadable dirs return an empty set.
func ScanUnwiredStubMethods(dir string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, "_gen.go") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, name))
		if rerr != nil {
			continue
		}
		for _, m := range UnwiredStubMarkerRE.FindAllStringSubmatch(string(data), -1) {
			sym := m[1]
			method := sym
			if i := strings.LastIndex(sym, "."); i >= 0 {
				method = sym[i+1:]
			}
			out[method] = true
		}
	}
	return out
}
