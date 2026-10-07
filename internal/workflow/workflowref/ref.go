// Copyright (c) 2025 Reliant Labs

// Package workflowref is the one definition of how a workflow reference names
// a workflow, and of where a project's workflows and their scenarios live.
//
// Every surface that turns a ref into a workflow calls it: the CLI
// (`reliant workflow validate`, `validate-tree`, `scenario run|list`), the
// daemon that syncs .reliant/workflows to the server, run start, the runtime's
// LoadWorkflow activity, and the scenario RPCs and agent tools. Before it
// existed each of those carried its own copy of the rule, and they drifted:
// the CLI resolved project://X by file name while the app used the name:
// field, and the runtime did not strip the scheme at all (issue #623).
//
// The rules:
//
//   - builtin://<name> names a workflow embedded in reliant, and nothing else.
//   - project://<name>, and a bare <name>, address a workflow by its name:
//     field, compared as a Slug. The caller's own workflows are consulted
//     first, then the project's. A workflow's FILE name is never an address.
//   - A project's workflows are the top-level *.yaml / *.yml files in
//     .reliant/workflows. Their scenarios live in
//     .reliant/workflows/<slug>/scenarios/<scenario>.yaml, one per file.
//
// Anything a project gets wrong — two files with one name, a file with no
// name, a name that is another file's file name, scenarios in the retired
// layouts — is a clear error rather than a guess.
//
//forge:exclude-contract: pure rule over values — a ref grammar, an index and a layout built from file contents; no I/O beyond the fs.FS a caller passes, and nothing to substitute
package workflowref

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Scheme prefixes a ref can carry.
const (
	BuiltinScheme = "builtin://"
	ProjectScheme = "project://"
)

// Kind is what a ref addresses.
type Kind int

const (
	// Builtin addresses a workflow embedded in reliant.
	Builtin Kind = iota + 1
	// Project addresses one of the caller's workflows or a project workflow,
	// by name.
	Project
)

// Ref is a parsed workflow reference.
type Ref struct {
	// Raw is the ref as written, without surrounding whitespace.
	Raw string
	// Kind is what it addresses.
	Kind Kind
	// Name is what follows the scheme: a builtin's file stem, or the name a
	// project ref addresses.
	Name string
}

// ErrNotFound is matched (errors.Is) by every "no such workflow" error this
// package returns, so a caller can tell a miss from a broken definition or a
// failed read.
var ErrNotFound = errors.New("workflow not found")

// notFoundError is a miss whose message is the specific reason. It matches
// ErrNotFound without repeating its text.
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string        { return e.msg }
func (e *notFoundError) Is(target error) bool { return target == ErrNotFound }

func notFound(format string, args ...any) error {
	return &notFoundError{msg: fmt.Sprintf(format, args...)}
}

// IsTemplate reports whether a ref is computed at run time ("{{inputs.flow}}")
// and so cannot be resolved statically.
func IsTemplate(ref string) bool {
	return strings.Contains(ref, "{{")
}

// Parse parses a workflow reference.
func Parse(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, errors.New("workflow ref is empty")
	}
	if IsTemplate(s) {
		return Ref{}, fmt.Errorf("workflow ref %q is a template; it resolves only at run time", s)
	}
	if name, ok := strings.CutPrefix(s, BuiltinScheme); ok {
		name = strings.TrimSpace(name)
		if name == "" {
			return Ref{}, fmt.Errorf("workflow ref %q names no builtin", s)
		}
		return Ref{Raw: s, Kind: Builtin, Name: name}, nil
	}
	name := s
	if rest, ok := strings.CutPrefix(s, ProjectScheme); ok {
		name = strings.TrimSpace(rest)
	} else if scheme, _, ok := strings.Cut(s, "://"); ok {
		return Ref{}, fmt.Errorf("workflow ref %q has unknown scheme %q: use %s<name> or %s<name>",
			s, scheme+"://", BuiltinScheme, ProjectScheme)
	}
	if Slug(name) == "" {
		return Ref{}, fmt.Errorf("workflow ref %q names no workflow", s)
	}
	return Ref{Raw: s, Kind: Project, Name: name}, nil
}

// Key is the ref's identity: two refs with the same Key name the same
// workflow however they are spelled ("project://Deploy", "deploy").
func (r Ref) Key() string {
	if r.Kind == Builtin {
		return BuiltinScheme + r.Name
	}
	return ProjectScheme + Slug(r.Name)
}

// RefKey is Key for a raw ref, or the trimmed ref itself when it does not
// parse (a template, an empty string).
func RefKey(raw string) string {
	ref, err := Parse(raw)
	if err != nil {
		return strings.TrimSpace(raw)
	}
	return ref.Key()
}

// ProjectSlug is the slug a project ref addresses, or "" when raw is not a
// project ref (a builtin, a template, nothing).
func ProjectSlug(raw string) string {
	ref, err := Parse(raw)
	if err != nil || ref.Kind != Project {
		return ""
	}
	return Slug(ref.Name)
}

var (
	slugInvalid = regexp.MustCompile(`[^a-z0-9-]`)
	slugDashes  = regexp.MustCompile(`-+`)
)

// Slug is the form a workflow name is compared in: lower case, spaces and
// underscores as hyphens, nothing but [a-z0-9-], no leading, trailing or
// repeated hyphens. "Blog Content_Pipeline!" and "blog-content-pipeline" are
// the same workflow.
func Slug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = slugInvalid.ReplaceAllString(s, "")
	s = slugDashes.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
