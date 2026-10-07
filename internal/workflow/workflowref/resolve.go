// Copyright (c) 2025 Reliant Labs
package workflowref

import (
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
}

// Resolved is a workflow a ref named.
type Resolved struct {
	Ref    Ref
	Source Source
	// Path is the project file it came from, relative to Dir, for
	// SourceProject.
	Path string
	// YAML is the definition as authored.
	YAML     []byte
	Workflow *reliantv1.Workflow
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

	slug := Slug(ref.Name)
	if src.User != nil {
		data, err := src.User(slug)
		if err != nil {
			return nil, err
		}
		if data != nil {
			return parsed(ref, SourceUser, "", data)
		}
	}
	entry, err := src.Project.Lookup(slug)
	if err != nil {
		return nil, err
	}
	return parsed(ref, SourceProject, entry.Path, entry.Content)
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
		return nil, fmt.Errorf("%s does not parse: %w", where, err)
	}
	return &Resolved{Ref: ref, Source: source, Path: path, YAML: data, Workflow: wf}, nil
}
