package codegen

// Migrating user-owned KCL from flattened config-block leaves to nested blocks.
//
// Composed config blocks used to be flattened into their root schema, so a
// per-env config.k authored a block leaf by its bare name
// (`github_client_id = "…"`) — or, where two blocks claimed the same leaf, by
// a forge-invented `<block>_<leaf>` — and a main.k read it the same way
// (`appcfg.app_config.github_redirect_uri`). Blocks are nested schemas now
// (config_block_flatten.go), so both spellings are "Cannot add member" /
// undefined-attribute errors against the new schema.
//
// forge changed the shape, so forge moves the references it caused to dangle —
// the same justification as ReconcileConfigModuleImports. The rewrite is a KEY
// rewrite only: `leaf =` becomes `block.leaf =` (KCL merges a path key into the
// nested schema's defaults), `x.app_config.leaf` becomes
// `x.app_config.block.leaf`. Values, comments and every other byte survive.
//
// It is deliberately conservative. A name is rewritten only when it is NOT a
// field of the root schema itself and resolves to exactly ONE block leaf; an
// ambiguous bare leaf (declared by two blocks — which could never have worked
// flat anyway) is left for the author, and the KCL error then names it.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/naming"
)

// ConfigInstanceBlocks describes one config instance a config.k declares: its
// variable name (`app_config`), and the root/block fields of its schema.
type ConfigInstanceBlocks struct {
	Var    string
	Fields []ConfigField
}

// blockRenames maps every flat spelling of a block leaf that is safe to move
// to its nested path: `leaf` (unambiguous, not a root field) and the legacy
// qualified `<block>_<leaf>`.
func blockRenames(fields []ConfigField) map[string]string {
	root := map[string]bool{}
	owners := map[string][]string{}
	for _, f := range fields {
		if f.KCLBlock == "" {
			root[f.Name] = true
			continue
		}
		owners[f.Name] = append(owners[f.Name], f.KCLBlock)
	}
	out := map[string]string{}
	for _, f := range fields {
		if f.KCLBlock == "" {
			continue
		}
		if legacy := f.KCLBlock + "_" + f.Name; !root[legacy] {
			out[legacy] = f.KCLPath()
		}
		if !root[f.Name] && len(owners[f.Name]) == 1 {
			out[f.Name] = f.KCLPath()
		}
	}
	return out
}

var (
	instanceOpenRE = regexp.MustCompile(`^([A-Za-z_]\w*)\s*(?::\s*[\w.]+\s*)?=\s*[\w.]*\s*\{\s*$`)
	memberKeyRE    = regexp.MustCompile(`^(\s+)([A-Za-z_]\w*)(\s*[=:|+]?=)`)
)

// MigrateConfigBlockReferences rewrites flat block-leaf references in each
// env's config.k and main.k under kclDirAbs to their nested paths, for the
// given instances. Returns the project-relative files it rewrote.
func MigrateConfigBlockReferences(kclDirAbs string, instances []ConfigInstanceBlocks) ([]string, error) {
	renames := map[string]map[string]string{}
	for _, in := range instances {
		if r := blockRenames(in.Fields); len(r) > 0 {
			renames[in.Var] = r
		}
	}
	if len(renames) == 0 {
		return nil, nil
	}
	envs, err := os.ReadDir(kclDirAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rewrote []string
	for _, e := range envs {
		if !e.IsDir() {
			continue
		}
		for _, name := range []string{"config.k", "main.k"} {
			path := filepath.Join(kclDirAbs, e.Name(), name)
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			out := migrateBlockKeys(string(src), renames)
			out = migrateBlockReads(out, renames)
			if out == string(src) {
				continue
			}
			if werr := writeUserScaffold(path, []byte(out)); werr != nil {
				return rewrote, fmt.Errorf("migrate %s: %w", path, werr)
			}
			rewrote = append(rewrote, filepath.Join(e.Name(), name))
		}
	}
	return rewrote, nil
}

// migrateBlockKeys rewrites the direct member keys of each known instance
// literal (`app_config: config_gen.AppConfig = {` … `}`). Only depth-1 keys of
// that literal are touched — a nested literal's keys belong to something else.
func migrateBlockKeys(src string, renames map[string]map[string]string) string {
	lines := strings.Split(src, "\n")
	var active map[string]string
	depth := 0
	for i, line := range lines {
		if active == nil {
			if m := instanceOpenRE.FindStringSubmatch(line); m != nil {
				if r, ok := renames[m[1]]; ok {
					active, depth = r, 1
				}
			}
			continue
		}
		code := line
		if j := strings.Index(code, "#"); j >= 0 {
			code = code[:j]
		}
		if depth == 1 {
			if m := memberKeyRE.FindStringSubmatchIndex(line); m != nil {
				key := line[m[4]:m[5]]
				if to, ok := active[key]; ok {
					lines[i] = line[:m[4]] + to + line[m[5]:]
				}
			}
		}
		depth += strings.Count(code, "{") - strings.Count(code, "}")
		if depth <= 0 {
			active = nil
		}
	}
	return strings.Join(lines, "\n")
}

// migrateBlockReads rewrites attribute reads `<var>.<flat>` to
// `<var>.<block>.<leaf>`, for every known instance var.
func migrateBlockReads(src string, renames map[string]map[string]string) string {
	vars := make([]string, 0, len(renames))
	for v := range renames {
		vars = append(vars, v)
	}
	sort.Strings(vars)
	for _, v := range vars {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(v) + `\.([A-Za-z_]\w*)\b`)
		r := renames[v]
		src = re.ReplaceAllStringFunc(src, func(m string) string {
			key := m[len(v)+1:]
			if to, ok := r[key]; ok {
				return v + "." + to
			}
			return m
		})
	}
	return src
}

// ConfigInstancesForMigration names the config.k instances forge scaffolds:
// `app_config` for the root AppConfig and `<snake(message)>` per binary.
func ConfigInstancesForMigration(rootFields []ConfigField, perBinary []BinaryConfigFields) []ConfigInstanceBlocks {
	var out []ConfigInstanceBlocks
	if len(rootFields) > 0 {
		out = append(out, ConfigInstanceBlocks{Var: "app_config", Fields: rootFields})
	}
	for _, bc := range perBinary {
		out = append(out, ConfigInstanceBlocks{Var: naming.ToSnakeCase(bc.MessageName), Fields: bc.Fields})
	}
	return out
}
