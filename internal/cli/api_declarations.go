package cli

// The APIs a project serves (forge.API): groundwork.
//
// An API is declared by its contract — proto services, or an OpenAPI 3
// document — on Bundle.apis. forge validates each when the env is loaded
// (validateAPIs: an OpenAPI spec must be OpenAPI 3.x, never Swagger 2) and
// lists them in `forge project audit --json`, with the proto API of a
// project that has proto services and declares none derived. Nothing is
// generated or served from an OpenAPI spec yet.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// The API kinds forge.API accepts.
const (
	APIKindProto   = "proto"
	APIKindOpenAPI = "openapi"
)

// APIEntity is one forge.API: an API the project serves, by its contract.
type APIEntity struct {
	// Name names the API.
	Name string `json:"name"`
	// Kind is the contract language: proto or openapi.
	Kind string `json:"kind"`
	// Spec is the repository-relative contract: the proto directory, or the
	// OpenAPI 3 document.
	Spec string `json:"spec"`
	// BasePath is where it is served, starting with "/".
	BasePath string `json:"base_path,omitempty"`
	// Database names the declared database it serves from.
	Database string `json:"database,omitempty"`
}

// errSwagger2 is the refusal of a Swagger 2 document declared as an OpenAPI API.
var errSwagger2 = errors.New("is Swagger 2.0, not OpenAPI 3")

// openAPIVersion reads the top-level `openapi` version of the document at
// path (YAML or JSON) and refuses one that is not OpenAPI 3.x. Only the
// top-level keys are decoded; the document is not otherwise validated.
func openAPIVersion(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a spec the project declares
	if err != nil {
		return "", err
	}
	var top map[string]yaml.Node
	if err := yaml.Unmarshal(data, &top); err != nil {
		return "", fmt.Errorf("does not parse as YAML or JSON: %w", err)
	}
	if v, ok := top["swagger"]; ok {
		return "", fmt.Errorf("%w (swagger: %q): convert it to OpenAPI 3 (e.g. `npx swagger2openapi %s -o openapi3.yaml`, or editor.swagger.io's Convert to OpenAPI 3)", errSwagger2, v.Value, filepath.Base(path))
	}
	v, ok := top["openapi"]
	if !ok {
		return "", errors.New(`has no top-level "openapi" version: an OpenAPI 3 document starts with openapi: "3.x.y"`)
	}
	if v.Kind != yaml.ScalarNode || !openAPI3RE.MatchString(v.Value) {
		return "", fmt.Errorf("declares openapi %q: forge reads OpenAPI 3.x documents", v.Value)
	}
	return v.Value, nil
}

var openAPI3RE = regexp.MustCompile(`^3\.\d+(\.\d+)?$`)

// validateAPIs checks what KCL cannot read cheaply: an OpenAPI API's spec is
// a file in the project that is an OpenAPI 3.x document, and a proto API's
// spec is a directory in it. Called wherever an env's declaration is loaded.
func validateAPIs(projectDir string, apis []APIEntity) error {
	var problems []string
	for _, a := range apis {
		p, err := projectContainedPath(projectDir, a.Spec)
		if err != nil {
			problems = append(problems, fmt.Sprintf("API %q: spec %s %v", a.Name, a.Spec, err))
			continue
		}
		switch a.Kind {
		case APIKindOpenAPI:
			info, err := os.Stat(p)
			switch {
			case errors.Is(err, fs.ErrNotExist):
				// The OS's own wording differs (Windows: "The system cannot
				// find the file specified") and names an absolute path.
				problems = append(problems, fmt.Sprintf("API %q: spec %s: no such file in the project", a.Name, a.Spec))
			case err != nil:
				problems = append(problems, fmt.Sprintf("API %q: spec %s: %v", a.Name, a.Spec, err))
			case info.IsDir():
				problems = append(problems, fmt.Sprintf("API %q: spec %s is a directory: an OpenAPI API's spec is one OpenAPI 3 document (YAML or JSON)", a.Name, a.Spec))
			default:
				if _, err := openAPIVersion(p); err != nil {
					problems = append(problems, fmt.Sprintf("API %q: spec %s %v", a.Name, a.Spec, err))
				}
			}
		case APIKindProto:
			if info, err := os.Stat(p); err != nil || !info.IsDir() {
				problems = append(problems, fmt.Sprintf("API %q: spec %s is not a directory: a proto API's spec is the directory holding its service protos (e.g. \"proto\")", a.Name, a.Spec))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("forge.API declarations are wrong:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// errSpecEscapes is projectContainedPath's error for a spec whose resolved
// path leaves the project.
var errSpecEscapes = errors.New("resolves outside the project (through a symlink): a spec is a file or directory in the repository")

// projectContainedPath joins rel (project-relative, slash-separated) onto
// projectDir and refuses it when its target, symlinks resolved, is not inside
// the project. KCL refuses an absolute or `..` spec lexically; this is the
// check a symlink cannot get past, made before anything is stat'ed or read.
// A path that does not exist is returned as joined, so the caller's stat
// reports it.
func projectContainedPath(projectDir, rel string) (string, error) {
	p := filepath.Join(projectDir, filepath.FromSlash(rel))
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
		return "", err
	}
	root, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return "", err
	}
	in, err := filepath.Rel(root, target)
	if err != nil || in == ".." || strings.HasPrefix(in, ".."+string(filepath.Separator)) || filepath.IsAbs(in) {
		return "", errSpecEscapes
	}
	return target, nil
}

// API origins, as `forge project audit --json` reports them.
const (
	originKCL     = "kcl"
	originDerived = "derived"
)

// projectAPI is one API of the project as audit lists it.
type projectAPI struct {
	APIEntity
	Origin string   `json:"origin"`
	Envs   []string `json:"envs,omitempty"`
}

// unionDeclaredAPIs unions the APIs every env declares (perEnv: env → its
// declaration), sorted by name. An API declared differently in two envs is an
// error: its contract is the project's.
func unionDeclaredAPIs(perEnv map[string]*KCLEntities) ([]projectAPI, error) {
	envs := make([]string, 0, len(perEnv))
	for env := range perEnv {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	byName := map[string]*projectAPI{}
	for _, env := range envs {
		e := perEnv[env]
		if e == nil {
			continue
		}
		for _, a := range e.APIs {
			a.Spec = cleanRepoPath(a.Spec)
			if prev, ok := byName[a.Name]; ok {
				if prev.APIEntity != a {
					return nil, fmt.Errorf("API %q is declared differently in env %s than in env %s: an API's contract is the project's, so declare it identically in every env",
						a.Name, env, strings.Join(prev.Envs, ", "))
				}
				prev.Envs = append(prev.Envs, env)
				continue
			}
			byName[a.Name] = &projectAPI{APIEntity: a, Origin: originKCL, Envs: []string{env}}
		}
	}
	out := make([]projectAPI, 0, len(byName))
	for _, a := range byName {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// withDerivedProtoAPI adds the proto API of a project that has proto services
// (hasProtoServices) and declares no proto API: {name: <project>, kind:
// proto, spec: proto}.
func withDerivedProtoAPI(apis []projectAPI, project string, hasProtoServices bool) []projectAPI {
	if !hasProtoServices {
		return apis
	}
	for _, a := range apis {
		if a.Kind == APIKindProto {
			return apis
		}
	}
	return append(apis, projectAPI{APIEntity: APIEntity{Name: project, Kind: APIKindProto, Spec: "proto"}, Origin: originDerived})
}

// kclAPIDeclarationRE matches KCL source that can declare an API: the
// cheap pre-check in front of evaluating every env.
var kclAPIDeclarationRE = regexp.MustCompile(`(?m)^[^#\n]*(\bapis\s*=|\bAPI\s*\{)`)

// kclMayDeclareAPIs reports whether any .k file under projectDir/deploy/kcl
// can declare an API.
func kclMayDeclareAPIs(projectDir string) bool {
	return kclSourceMatches(projectDir, kclAPIDeclarationRE)
}

// projectAPIs lists the project's APIs: every env's declarations, unioned,
// plus the derived proto API.
func projectAPIs(projectDir, project string) ([]projectAPI, error) {
	perEnv, err := declaredEnvs(projectDir, kclMayDeclareAPIs)
	if err != nil {
		return nil, err
	}
	apis, err := unionDeclaredAPIs(perEnv)
	if err != nil {
		return nil, err
	}
	return withDerivedProtoAPI(apis, project, projectDefinesConnectServices(projectDir)), nil
}

// auditResources is `forge project audit`'s resources category: the
// project's databases and APIs, each with where it came from — `kcl` (an env
// declaration, with the envs that declare it), `forge.yaml` (the legacy
// `database:` block) or `derived` (db/migrations on disk; the proto API of a
// project with proto services and none declared). Informational: `error`
// only when the declarations cannot be read.
func auditResources(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	project := filepath.Base(projectDir)
	if cfg != nil && cfg.Name != "" {
		project = cfg.Name
	}
	var problems []string

	databases := []map[string]any{}
	dbs, err := config.ProjectDatabases(projectDir)
	if err != nil {
		problems = append(problems, err.Error())
	}
	primary, _ := config.PrimaryDatabase(dbs)
	for _, d := range dbs {
		origin := originKCL
		if d.Source == config.DatabaseSourceForgeYAML {
			origin = config.DatabaseSourceForgeYAML
			if !config.DatabaseIn(projectDir).DeclaresDatabase() {
				if !dirExists(filepath.Join(projectDir, filepath.FromSlash(d.MigrationsPath()))) {
					continue // nothing declared and nothing on disk: no database
				}
				origin = originDerived
			}
		}
		entry := map[string]any{
			"name":           d.Name,
			"dialect":        d.Dialect,
			"migrations_dir": d.MigrationsPath(),
			"driver":         d.Config(config.DatabaseConfig{}).MigrationTool(),
			"orm_package":    d.ORM.PackageDir(),
			"origin":         origin,
			"primary":        d.Name == primary.Name,
		}
		if len(d.Envs) > 0 {
			entry["envs"] = d.Envs
		}
		databases = append(databases, entry)
	}

	apis, err := projectAPIs(projectDir, project)
	if err != nil && !slices.Contains(problems, err.Error()) {
		problems = append(problems, err.Error())
	}
	if apis == nil {
		apis = []projectAPI{}
	}

	cat := audittype.Category{
		Status:  audittype.StatusOK,
		Summary: fmt.Sprintf("%d database(s), %d API(s)", len(databases), len(apis)),
		Details: map[string]any{"databases": databases, "apis": apis},
	}
	if len(problems) > 0 {
		cat.Status = audittype.StatusError
		cat.Summary = "the project's databases or APIs cannot be read: " + strings.Join(problems, "; ")
	}
	return cat
}
