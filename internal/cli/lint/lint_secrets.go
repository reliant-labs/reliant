// File: internal/cli/lint/lint_secrets.go
//
// secrets — `forge lint --secrets`: the rules about platform secrets
// (store-backed forge.ExternalSecrets, the External Secrets Operator objects
// they render, forge.OpenBao grants) that kcl/schema.k cannot see because they
// span a whole rendered environment. The rules and what each one refuses are in
// internal/lint/secrets; this file renders each env, builds the facts, and
// reports.
//
// GATING, with an escape in every message. A finding is an error unless the rule
// says otherwise (the escrow rule warns; the unreferenced-key tool is info), and
// every message names the KCL line and the directive that waives it.
//
// OPT-IN BY ADOPTION. A project whose KCL declares no store source, OpenBao or
// ESO component is not rendered and not judged.
package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/lint/secrets"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// ruleSecretsNotRendered is reported when an env of an adopting project cannot
// be rendered, so its secrets could not be judged. It warns: `forge env render`
// owns the render error, and failing here would report it twice.
const ruleSecretsNotRendered = "secrets-env-not-rendered"

// secretsLaneResult is what the lane found and how much it judged.
type secretsLaneResult struct {
	findings []finding.Finding
	// judged is the number of envs that use the platform secrets model.
	judged int
	// adopted says whether the project's KCL declares any of it at all.
	adopted bool
}

// readKCLFiles reads every .k file under dir as project-relative sources.
func readKCLFiles(projectDir, dir string) []secrets.SourceFile {
	var out []secrets.SourceFile
	root := filepath.Join(projectDir, dir)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".k" {
			return nil
		}
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(projectDir, p)
		if rerr != nil {
			return nil
		}
		out = append(out, secrets.SourceFile{Path: filepath.ToSlash(rel), Text: string(raw)})
		return nil
	})
	return out
}

// renderSecretsEnv renders one env into the facts the rules judge: the render's
// declarations, the expanded manifest stream, and the Secrets its workloads read.
func renderSecretsEnv(projectDir, kclDir, env string) (secrets.Env, error) {
	mainK := filepath.Join(projectDir, kclDir, env, "main.k")
	e := secrets.Env{Name: env, File: filepath.ToSlash(filepath.Join(kclDir, env, "main.k"))}
	raw, err := kclrender.Run(projectDir, mainK, []string{
		"image_tag=" + strconv.Quote("lint"), "namespace=" + strconv.Quote(""), "env=" + strconv.Quote(env),
	})
	if err != nil {
		return e, err
	}
	if e.Render, err = secrets.ParseRender(raw); err != nil {
		return e, err
	}
	stream, err := cluster.ExtractManifests(raw)
	if err != nil {
		return e, err
	}
	place := cluster.RefPlacement{Namespace: ""}
	if ct := e.Render.ClusterTarget; ct != nil {
		place.Namespace, place.Context = ct.Namespace, ct.Cluster
	}
	for _, raw := range cluster.SplitManifestDocs(stream) {
		var body map[string]any
		if yaml.Unmarshal([]byte(raw), &body) != nil || body == nil {
			continue
		}
		doc := secrets.Doc(body)
		e.Docs = append(e.Docs, doc)
		e.Demands = append(e.Demands, secretDemands(raw, doc, place)...)
	}
	return e, nil
}

// secretDemands lists the Secrets one document reads. An imagePullSecret is a
// registry credential the cluster provisions out of band, not an application
// Secret a bundle is expected to declare, so it is left to the preflight.
func secretDemands(rawDoc string, doc secrets.Doc, place cluster.RefPlacement) []secrets.Demand {
	refs := cluster.CollectManifestRefs(rawDoc, place)
	if len(refs.Secrets) == 0 {
		return nil
	}
	meta, _ := doc["metadata"].(map[string]any)
	who := fmt.Sprintf("%v/%v", doc["kind"], meta["name"])
	labels, _ := meta["labels"].(map[string]any)
	_, owned := labels["forge.dev/component-owner"]
	var out []secrets.Demand
	for ref, keys := range refs.Secrets {
		if _, pull := refs.ImagePullSecrets[ref]; pull || ref.Name == "" {
			continue
		}
		d := secrets.Demand{Workload: who, Namespace: ref.Namespace, Secret: ref.Name, Component: owned}
		for k := range keys {
			if k == "" {
				d.Whole = true
			} else {
				d.Keys = append(d.Keys, k)
			}
		}
		sort.Strings(d.Keys)
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Secret < out[j].Secret })
	return out
}

// secretsFindings runs the lane over every env of the project.
func secretsFindings(projectDir string, cfg *config.ProjectConfig) (secretsLaneResult, error) {
	kclDir := deployKCLDirFor(cfg)
	files := readKCLFiles(projectDir, kclDir)
	var res secretsLaneResult
	if !secrets.MentionsPlatformSecrets(files) {
		return res, nil
	}
	res.adopted = true
	loc := secrets.NewLocator(files)

	entries, err := os.ReadDir(filepath.Join(projectDir, kclDir))
	if err != nil {
		return res, nil
	}
	var all []finding.Finding
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, kclDir, ent.Name(), "main.k")); err != nil {
			continue
		}
		env, err := renderSecretsEnv(projectDir, kclDir, ent.Name())
		if err != nil {
			all = append(all, finding.Finding{
				Rule: ruleSecretsNotRendered, Severity: finding.SeverityWarning, File: env.File, Line: 1,
				Message:     fmt.Sprintf("env %s did not render, so its secrets were not judged: %v", ent.Name(), firstLine(err)),
				Remediation: fmt.Sprintf("Fix the render (`forge env render %s` shows it). Escape: `# forge:lint-disable-file %s: <why>` in %s.", ent.Name(), ruleSecretsNotRendered, env.File),
			})
			continue
		}
		if env.Adopted() {
			res.judged++
		}
		all = append(all, secrets.Check(env, loc)...)
	}

	// lint.rules grades these in the outcome engine (lint_rules.go).
	applied := suppress.ApplyFiles(projectDir, all, os.ReadFile)
	kept := append(slices.Clone(applied.Kept), applied.Violations...)
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].File != kept[j].File {
			return kept[i].File < kept[j].File
		}
		if kept[i].Line != kept[j].Line {
			return kept[i].Line < kept[j].Line
		}
		return kept[i].Rule < kept[j].Rule
	})
	res.findings = kept
	return res, nil
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// collectSecretsJSON is the JSON arm. Error findings gate.
func collectSecretsJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	res, err := secretsFindings(rc.cwd, rc.cfg)
	if err != nil {
		return nil, false, err
	}
	out := findingsToJSON(res.findings)
	return out, anyErrorFinding(out), nil
}
