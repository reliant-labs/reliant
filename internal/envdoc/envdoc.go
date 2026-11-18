// Package envdoc reads an env document: an environment declared as the
// document a KCL env renders to, written by hand as YAML or JSON at
// deploy/envs/<env>.yaml (or .yml / .json), instead of as KCL.
//
// The document conforms to the env schema (schema/env.v1.schema.json,
// apiVersion forge.dev/env/v1), which is generated from the Go types forge
// decodes an env with. [Load] validates it, applies the lowering a KCL render
// would have applied and a hand-written document may omit (lower.go), and
// returns the same `{"output": …}` bytes a KCL evaluation returns — so the two
// render seams (internal/cli's entity render and internal/cluster's manifest
// render) hand it to the parsers they already have, and every command reads
// one model whichever way the env was declared.
//
// docs/design/env-document.md records the design and each lowering decision.
package envdoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/schemagen"
	forgeschema "github.com/reliant-labs/forge/schema"
)

// APIVersion is the apiVersion every env document carries.
const APIVersion = "forge.dev/env/v1"

// Dir is the project-relative directory env documents live in.
const Dir = "deploy/envs"

// extensions are the file extensions an env document may have, in the order
// a name is reported.
var extensions = []string{".yaml", ".yml", ".json"}

// ErrDeclaredTwice is the load error for an env that both a KCL tree and an
// env document declare.
var ErrDeclaredTwice = errors.New("env declared twice")

// Candidates are the files that would declare env's document, present or
// not: what a cache keyed on env's declaration must stamp, so adding,
// editing or removing one invalidates it.
func Candidates(projectDir, env string) []string {
	out := make([]string, 0, len(extensions))
	for _, ext := range extensions {
		out = append(out, filepath.Join(projectDir, Dir, env+ext))
	}
	return out
}

// Path returns the env document declaring env in the project at projectDir,
// "" when there is none. Two documents for one env (prod.yaml and prod.json)
// are an error naming both.
func Path(projectDir, env string) (string, error) {
	if projectDir == "" || env == "" || strings.ContainsAny(env, `/\`) || strings.HasPrefix(env, ".") {
		return "", nil
	}
	var found []string
	for _, ext := range extensions {
		p := filepath.Join(projectDir, Dir, env+ext)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("env %q has %d env documents (%s): keep one", env, len(found), strings.Join(relAll(projectDir, found), ", "))
}

// List returns the envs declared by env documents, sorted.
func List(projectDir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(projectDir, Dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ext := filepath.Ext(e.Name())
		for _, want := range extensions {
			if ext == want {
				seen[strings.TrimSuffix(e.Name(), ext)] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// KCLMain is the KCL declaration of env, whose existence alongside a
// document is a load error.
func KCLMain(projectDir, env string) string {
	return filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
}

// ConflictError is the error for an env both a KCL tree and a document
// declare, nil when at most one does.
func ConflictError(projectDir, env, docPath string) error {
	if docPath == "" {
		return nil
	}
	if _, err := os.Stat(KCLMain(projectDir, env)); err != nil {
		return nil
	}
	return fmt.Errorf("%w: env %q is declared by both %s and %s\n"+
		"fix: an env is declared in ONE place — keep the KCL (deploy/kcl/%s/) or the env document, and delete the other",
		ErrDeclaredTwice, env, rel(projectDir, KCLMain(projectDir, env)), rel(projectDir, docPath), env)
}

// Options are the facts a render binds that a document cannot hold.
type Options struct {
	// Release is the release being built or deployed ("" when unbound): the
	// service.version every derived OTEL_RESOURCE_ATTRIBUTES carries.
	Release string
	// ImageTag is the tag the render binds (KCL's `-D image_tag`), "" when
	// unbound: the document's own image_tag, else the env's default, applies.
	ImageTag string
	// ImageDigests is the per-image digest map a deploy captured from its
	// pushes (KCL's `-D image_digests`), keyed as kcl/lib/images.k
	// digest_key keys it.
	ImageDigests map[string]string
	// WorkloadPins are the exact per-slot references a release snapshot
	// binds (KCL's `-D forge_workload_image_digests`): workload → slot
	// ("container:<name>", "init:<name>", "input:<key>") → reference.
	WorkloadPins map[string]map[string]string
	// Immutable refuses any image slot WorkloadPins does not bind (KCL's
	// `-D forge_immutable_release`): a recorded release deploys exactly the
	// references it recorded.
	Immutable bool
}

// Load reads, validates and lowers the env document declaring env, returning
// the render-shaped `{"output": …}` JSON. ok is false when the env has no
// document (the caller renders KCL).
func Load(projectDir, env string, opts Options) (out []byte, ok bool, err error) {
	path, err := Path(projectDir, env)
	if err != nil || path == "" {
		return nil, false, err
	}
	if err := ConflictError(projectDir, env, path); err != nil {
		return nil, true, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, true, err
	}
	doc, err := Parse(raw, filepath.Ext(path) == ".json")
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", rel(projectDir, path), err)
	}
	if err := Check(doc, env); err != nil {
		return nil, true, fmt.Errorf("%s is not a valid env document:\n%w", rel(projectDir, path), err)
	}
	if err := Lower(doc, opts); err != nil {
		return nil, true, fmt.Errorf("%s: %w", rel(projectDir, path), err)
	}
	b, err := json.Marshal(map[string]any{"output": doc})
	return b, true, err
}

// Parse decodes a document's bytes (YAML, or JSON when isJSON) into the
// generic form [Check] and [Lower] work on, numbers kept exact.
func Parse(raw []byte, isJSON bool) (map[string]any, error) {
	data := raw
	if !isJSON {
		j, err := yaml.YAMLToJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("parse YAML: %w", err)
		}
		data = j
	}
	if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
		return nil, fmt.Errorf("the document is empty: an env document starts with `apiVersion: %s`", APIVersion)
	}
	v, err := schemagen.DecodeJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	doc, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the document is not an object: an env document starts with `apiVersion: %s`", APIVersion)
	}
	return doc, nil
}

var (
	compiledOnce sync.Once
	compiled     *schemagen.Compiled
	compileErr   error
)

// Schema is the compiled env schema this binary validates with.
func Schema() (*schemagen.Compiled, error) {
	compiledOnce.Do(func() { compiled, compileErr = schemagen.Compile(forgeschema.EnvV1()) })
	return compiled, compileErr
}

// Check validates a parsed document: its apiVersion, its env (which must be
// the env its file names), and the schema. Every failure is reported, each
// naming the path and the field.
func Check(doc map[string]any, env string) error {
	var errs schemagen.Errors
	switch v := doc["apiVersion"].(type) {
	case nil:
		errs = append(errs, schemagen.Error{Path: "apiVersion", Message: fmt.Sprintf("required: an env document declares `apiVersion: %s`", APIVersion)})
	case string:
		if v != APIVersion {
			errs = append(errs, schemagen.Error{Path: "apiVersion", Message: fmt.Sprintf("%q is not one this forge reads (it reads %s)", v, APIVersion)})
		}
	}
	if got, ok := doc["env"].(string); ok && env != "" && got != env {
		errs = append(errs, schemagen.Error{Path: "env", Message: fmt.Sprintf("%q, but the file declares env %q: the file name is the env", got, env)})
	}
	s, err := Schema()
	if err != nil {
		return err
	}
	errs = append(errs, s.Validate(doc)...)
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// rel is p relative to the project, with forward slashes on every OS, for
// messages: they name a path the same way (deploy/envs/prod.yaml) on Windows
// as everywhere else, and as the fix hints beside them spell it.
func rel(projectDir, p string) string {
	if r, err := filepath.Rel(projectDir, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}

func relAll(projectDir string, ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = rel(projectDir, p)
	}
	return out
}
