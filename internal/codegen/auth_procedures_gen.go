package codegen

import (
	"fmt"
	"sort"
	"time"

	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"
)

// Who may call without credentials, and where that is written down.
//
// `(forge.v1.method).auth_required` used to gate nothing. It fed
// `forge project graph` and stopped there, while the runtime
// answer came from a hand-written map of procedure strings in the project's
// own pkg/middleware — two declaration surfaces for one fact, able to
// disagree, only one of them load-bearing. A measured run shipped an app whose
// twenty CRUD RPCs all declared auth_required: true and every one of which
// served anonymous callers, and nothing in the project was wrong by its own
// lights.
//
// The proto is now the declaration and this is its projection: the open set is
// DERIVED from the annotation, the interceptor runs fail-closed
// (AnonymousOK: false) against it, and the hand-written map is gone. That
// deletes a mechanism rather than adding one — the enforcement seam
// (authn.Policy.Unauthenticated + AnonymousOK) already existed and was simply
// wired to the wrong source.
//
// What this does NOT decide is what an authenticated caller may then DO, or
// which rows they may see. That is application policy, it lives in the
// handler, and no annotation can express it.

// OpenProcedureImport is one connect package the generated file imports.
type OpenProcedureImport struct {
	Alias string // "demov1connect"
	Path  string // "example.com/app/gen/services/demo/v1/demov1connect"
}

// OpenProcedureEntry is one RPC the project declared public.
type OpenProcedureEntry struct {
	Const   string // "demov1connect.DemoServiceGetStatusProcedure"
	Service string // "demo.v1.DemoService"
	Method  string // "GetStatus"
}

// ProcedureDeadlineEntry is one RPC that declared its own server-side
// deadline, `(forge.v1.method).timeout`.
type ProcedureDeadlineEntry struct {
	Const    string // "demov1connect.DemoServiceExportProcedure"
	Service  string // "demo.v1.DemoService"
	Method   string // "Export"
	Budget   string // the Go expression: "2 * time.Minute"
	Declared string // as the proto said it: "2m0s"
}

// OpenProceduresTemplateData drives middleware-procedures.go.tmpl.
type OpenProceduresTemplateData struct {
	Imports   []OpenProcedureImport
	Open      []OpenProcedureEntry
	Deadlines []ProcedureDeadlineEntry
}

// BuildOpenProcedures projects the services' auth_required declarations onto
// the generated open-procedure set.
//
// Only `auth_required: false` puts an RPC here. The descriptor defaults an
// unannotated RPC to true (fail-closed), so silence is never a way to open an
// endpoint — publishing one is an edit to the proto that a reviewer sees.
//
// Streaming RPCs are included on the same terms as unary ones: the
// interceptor gates the procedure, and a stream is a procedure.
//
// The same pass projects `(forge.v1.method).timeout` onto ProcedureDeadlines,
// the per-procedure budgets of the chain's Deadline slot. An rpc that declares
// none is absent, and gets the slot's default.
func BuildOpenProcedures(services []ServiceDef, modulePath string) OpenProceduresTemplateData {
	var data OpenProceduresTemplateData
	seenImport := map[string]string{} // path -> alias

	for _, svc := range services {
		var open, timed []Method
		for _, m := range svc.Methods {
			if !m.AuthRequired {
				open = append(open, m)
			}
			if m.Timeout != 0 {
				timed = append(timed, m)
			}
		}
		if len(open) == 0 && len(timed) == 0 {
			continue
		}
		alias, path := connectPackageFor(svc, modulePath)
		if path == "" {
			continue
		}
		if _, ok := seenImport[path]; !ok {
			seenImport[path] = alias
			data.Imports = append(data.Imports, OpenProcedureImport{Alias: alias, Path: path})
		}
		alias = seenImport[path]
		for _, m := range open {
			data.Open = append(data.Open, OpenProcedureEntry{
				Const:   alias + "." + naming.ConnectProcedureConst(svc.Name, m.Name),
				Service: svc.Package + "." + svc.Name,
				Method:  m.Name,
			})
		}
		for _, m := range timed {
			data.Deadlines = append(data.Deadlines, ProcedureDeadlineEntry{
				Const:    alias + "." + naming.ConnectProcedureConst(svc.Name, m.Name),
				Service:  svc.Package + "." + svc.Name,
				Method:   m.Name,
				Budget:   durationExpr(m.Timeout),
				Declared: m.Timeout.String(),
			})
		}
	}

	// Deterministic output: the same project renders the same bytes, so a
	// re-run is not a diff.
	sort.Slice(data.Imports, func(i, j int) bool { return data.Imports[i].Path < data.Imports[j].Path })
	sort.Slice(data.Open, func(i, j int) bool { return data.Open[i].Const < data.Open[j].Const })
	sort.Slice(data.Deadlines, func(i, j int) bool { return data.Deadlines[i].Const < data.Deadlines[j].Const })
	return data
}

// durationExpr spells d as the Go expression a reader would write: the
// largest unit that divides it exactly ("90 * time.Second", "-1 *
// time.Second"), or a raw count of nanoseconds.
func durationExpr(d time.Duration) string {
	for _, u := range []struct {
		d    time.Duration
		name string
	}{
		{time.Hour, "time.Hour"},
		{time.Minute, "time.Minute"},
		{time.Second, "time.Second"},
		{time.Millisecond, "time.Millisecond"},
		{time.Microsecond, "time.Microsecond"},
	} {
		if d%u.d == 0 {
			return fmt.Sprintf("%d * %s", int64(d/u.d), u.name)
		}
	}
	return fmt.Sprintf("time.Duration(%d)", int64(d))
}

// connectPackageFor resolves the Go import path and package alias of a
// service's generated connect package. It is the same derivation the
// bootstrap testing generator uses — off the proto's declared go_package,
// falling back to forge's layout convention only when the descriptor carries
// neither field (synthetic fixtures, pre-descriptor scaffolds).
func connectPackageFor(svc ServiceDef, modulePath string) (alias, path string) {
	if svc.GoPackage != "" && svc.PkgName != "" {
		alias = naming.ConnectPackage(svc.PkgName)
		return alias, svc.GoPackage + "/" + alias
	}
	if modulePath == "" {
		return "", ""
	}
	synth := naming.ServicePackage(svc.Name)
	alias = synth + "v1connect"
	return alias, modulePath + "/gen/services/" + synth + "/v1/" + alias
}

// RenderOpenProcedures renders pkg/middleware/procedures_gen.go.
func RenderOpenProcedures(services []ServiceDef, modulePath string) ([]byte, error) {
	content, err := templates.ProjectTemplates().Render("middleware-procedures.go.tmpl",
		BuildOpenProcedures(services, modulePath))
	if err != nil {
		return nil, fmt.Errorf("render middleware-procedures.go.tmpl: %w", err)
	}
	return content, nil
}
