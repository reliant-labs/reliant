// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"errors"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// Source is where a ref resolved.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceProject Source = "project"
)

// Sources are what a project ref can resolve against, consulted in this order.
// A nil source is skipped: the CLI has no user workflows, and a ref resolved
// outside any project has no project index.
type Sources struct {
	// User returns the definition of the caller's own workflow with this slug,
	// or (nil, nil) when they have none. Which of their workflows count (only
	// complete ones, or a builder test run's draft) is the caller's policy.
	// An error is final: a workflow of theirs that cannot run (a draft, a
	// failed read) shadows a project workflow of the same name rather than
	// silently running a different definition.
	User func(slug string) ([]byte, error)
	// Project is the project's workflow index.
	Project *Index
	// ProjectFirst consults Project before User: an automation's precedence.
	// An automation runs what the project declares, so the owner's own
	// workflow of the same name must not silently replace it (it is still
	// what a bare name resolves to when the project has none). A project
	// workflow that is broken fails closed rather than falling through to
	// the owner's. Interactive runs keep the default, the caller's own
	// workflow first ("yours wins for you").
	ProjectFirst bool
}

// Resolved is a workflow a ref named.
type Resolved struct {
	Ref    Ref
	Source Source
	// Path is the project file it came from, for SourceProject: relative to
	// Dir for the project root's own workflows, to the project root for a
	// nested repo's (Entry.Location).
	Path string
	// YAML is the definition as authored.
	YAML     []byte
	Workflow *reliantv1.Workflow
	// Shadowed names the definition of the same name the precedence passed
	// over ("your account workflow \"release\"", or the project file), or ""
	// when there was none. Surfaces show it so a shadow is never silent.
	Shadowed string
}

// Resolve turns a ref into the workflow it names. It is the one resolution
// rule: builtin:// from the embedded builtins; project:// or a bare name
// from the caller's own workflows, then the project's, by name:.
//
// The error never repeats the ref (a caller reports it in the context it
// has: a node, a chain of refs). A miss matches ErrNotFound.
func Resolve(raw string, src Sources) (*Resolved, error) {
	ref, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if ref.Kind == Builtin {
		data, err := BuiltinYAML(ref.Name)
		if err != nil {
			return nil, err
		}
		return parsed(ref, SourceBuiltin, "", data)
	}

	slug := QualifiedSlug(ref.Name)
	// A repo-qualified name addresses a project workflow and nothing else:
	// the caller's own workflows have no repo.
	_, _, qualified := SplitQualifiedSlug(slug)
	user := src.User
	if qualified {
		user = nil
	}
	if src.ProjectFirst {
		entry, err := src.Project.Lookup(slug)
		if err == nil {
			resolved, err := parsed(ref, SourceProject, entry.Location(), entry.Content)
			if err == nil && user != nil {
				// Only to say so: an account workflow that cannot be read or
				// run (an unfinished draft is an error here) is no reason to
				// fail a run of the project's, and is still a same-named one.
				if data, userErr := user(slug); userErr != nil || data != nil {
					resolved.Shadowed = fmt.Sprintf("your account workflow %q", slug)
				}
			}
			return resolved, err
		}
		if !errors.Is(err, ErrNotFound) || user == nil {
			return nil, err
		}
		data, userErr := user(slug)
		if userErr != nil {
			return nil, userErr
		}
		if data == nil {
			return nil, err
		}
		return parsed(ref, SourceUser, "", data)
	}
	if user != nil {
		data, err := user(slug)
		if err != nil {
			return nil, err
		}
		if data != nil {
			resolved, err := parsed(ref, SourceUser, "", data)
			if err == nil {
				if entry, lookupErr := src.Project.Lookup(slug); lookupErr == nil {
					resolved.Shadowed = "the project's " + RelativePath(entry.Repo, entry.Path)
				}
			}
			return resolved, err
		}
	}
	entry, err := src.Project.Lookup(slug)
	if err != nil {
		return nil, err
	}
	return parsed(ref, SourceProject, entry.Location(), entry.Content)
}

// BuiltinYAML returns the definition of the builtin workflow called name:
// an internal utility workflow, or one embedded from internal/workflow/builtin.
func BuiltinYAML(name string) ([]byte, error) {
	if data := builtin.GetInternalWorkflowYAML(name); data != nil {
		return data, nil
	}
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	if err != nil {
		return nil, notFound("no builtin workflow is named %q", name)
	}
	return data, nil
}

func parsed(ref Ref, source Source, path string, data []byte) (*Resolved, error) {
	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		where := ref.Raw
		if path != "" {
			where = path
		}
		err = fmt.Errorf("%s does not parse: %w", where, err)
		if source == SourceProject {
			return nil, &FileError{Path: ProjectRelative(path), Content: data, Err: err}
		}
		return nil, err
	}
	return &Resolved{Ref: ref, Source: source, Path: path, YAML: data, Workflow: wf}, nil
}
