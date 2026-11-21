package codegen

import "sort"

// DeclaredRoot is one `//forge:root` struct as internal/app declares it — the
// inventory `forge project audit` and `forge project shapes` report. It is read
// by the same discovery the composition graph is built from
// (discoverAppDecls), so the two can never disagree about which roots exist.
//
// It is the declaration only: nothing is resolved, so a project whose graph
// does not assemble (a missing provider, a cycle) still lists its roots.
type DeclaredRoot struct {
	// Name is the root's name: the `//forge:root <name>` argument, else the
	// struct's name in kebab case.
	Name string
	// TypeName is the root struct's Go name.
	TypeName string
	// Command is the command path that runs the root: the `command=a,b`
	// option, else [Name].
	Command []string
	// Migrate is the declared `migrate=` option (apply, wait or none). Empty
	// means the default: apply when the root reaches the database, otherwise
	// nothing.
	Migrate string
	// Adopt is the `adopt` option.
	Adopt bool
	// Short is the first paragraph of the struct's doc comment: its
	// command's one-line help.
	Short string
	// Fields are the root's field names, in declaration (stop) order.
	Fields []string
	// RootOnly are the providers marked `//forge:root-only` that name this
	// root, sorted by name.
	RootOnly []RootOnlyProvider
	// File (project-relative, slash-separated) and Line locate the struct.
	File string
	Line int
}

// RootOnlyProvider is a provider func marked `//forge:root-only`.
type RootOnlyProvider struct {
	// Name is the provider func's name, New<X>.
	Name string
	File string
	Line int
}

// DeclaredRoots returns every `//forge:root` struct declared in the owned
// internal/app files, sorted by name. A project that declares none (the
// generated default Server root) returns nil, as does one with no
// internal/app. The error is a discovery error — an unparsable file or a
// malformed directive — the same one `forge generate` would report.
func DeclaredRoots(projectDir string) ([]DeclaredRoot, error) {
	providers, roots, err := discoverAppDecls(projectDir)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, nil
	}
	rootOnly := map[string][]RootOnlyProvider{}
	for _, p := range providers {
		for _, r := range p.RootOnly {
			rootOnly[r] = append(rootOnly[r], RootOnlyProvider{Name: "New" + p.Name, File: p.File, Line: p.Line})
		}
	}
	out := make([]DeclaredRoot, 0, len(roots))
	for _, r := range roots {
		d := DeclaredRoot{
			Name:     r.Name,
			TypeName: r.TypeName,
			Command:  append([]string(nil), r.Command...),
			Migrate:  r.Migrate,
			Adopt:    r.Adopt,
			Short:    r.Short,
			File:     r.File,
			Line:     r.Line,
			RootOnly: rootOnly[r.Name],
		}
		for _, f := range r.Fields {
			d.Fields = append(d.Fields, f.Name)
		}
		sort.Slice(d.RootOnly, func(i, j int) bool { return d.RootOnly[i].Name < d.RootOnly[j].Name })
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
