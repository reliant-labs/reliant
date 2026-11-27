// File: internal/cli/lint/telemetry.go
//
// telemetry — `forge lint --telemetry`, and a step of every `forge lint`: the
// rules about declared telemetry (forge.Telemetry, pkg/telemetry) that its KCL
// checks and telemetry.Validate cannot judge, because each is about what the
// declaration COLLECTS rather than whether it is well formed:
//
//	telemetry-scrape-unbounded             a metrics scrape with no `allow`
//	telemetry-dashboard-metric-uncollected a declared dashboard draws a metric
//	                                       no scrape of its system keeps
//	telemetry-rate-on-gauge                rate/increase over a name nothing
//	                                       marks as a counter
//	telemetry-high-cardinality-risk        a scrape whose series ceiling was
//	                                       raised, or never narrowed, with no
//	                                       drop_labels
//
// The rules themselves are in telemetry_rules.go; this file renders each env,
// decodes its `output.telemetry` exactly as `forge env render` does, and
// reports.
//
// GATING. Only telemetry-dashboard-metric-uncollected is an error: a panel
// over a series the collector drops is empty in every env it ships to, with
// no other symptom. The other three are judgement calls a project can be right
// to make, so they warn at any pin (lint.rules raises them). All four joined in
// one release and warn first (lint_rollout_table.go).
//
// OPT-IN BY ADOPTION. A project whose KCL declares no forge.Telemetry is not
// rendered and not judged. An env that does not render is named in the lane's
// note and skipped: `forge env render` owns the render error.
package lint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/lint/secrets"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
	"github.com/reliant-labs/forge/pkg/telemetry"
)

// The rules the telemetry lane emits.
const (
	ruleTelemetryScrapeUnbounded   = "telemetry-scrape-unbounded"
	ruleTelemetryMetricUncollected = "telemetry-dashboard-metric-uncollected"
	ruleTelemetryRateOnGauge       = "telemetry-rate-on-gauge"
	ruleTelemetryHighCardinality   = "telemetry-high-cardinality-risk"
)

// telemetryDeclaration is how a KCL file declares a forge.Telemetry (or a
// project schema of that name): the schema instantiated. BrowserTelemetry, a
// frontend's own setting, has no word boundary before its `Telemetry`.
var telemetryDeclaration = regexp.MustCompile(`\bTelemetry\s*\{`)

// telemetryEnv is one env's rendered telemetry.
type telemetryEnv struct {
	Name string
	// File is the env's main.k, project-relative: where a finding lands when
	// its declaration cannot be found in the source.
	File    string
	Targets []telemetry.Target
}

// telemetryLaneResult is what the lane found and what it judged.
type telemetryLaneResult struct {
	findings []finding.Finding
	adopted  bool
	envs     int
	targets  int
	// unrendered are the envs that did not render, so were not judged.
	unrendered []string
}

// telemetryAdopted reports whether any KCL file declares a forge.Telemetry.
func telemetryAdopted(files []secrets.SourceFile) bool {
	for _, f := range files {
		for _, line := range strings.Split(f.Text, "\n") {
			if !isKCLCommentLine(line) && telemetryDeclaration.MatchString(line) {
				return true
			}
		}
	}
	return false
}

// decodeRenderedTelemetry reads `output.telemetry` from a render and returns
// it decoded and normalized, as KCLEntities.Telemetry holds it.
func decodeRenderedTelemetry(raw []byte) ([]telemetry.Target, error) {
	var wrapper struct {
		Output json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("parse render: %w", err)
	}
	body := raw
	if len(wrapper.Output) > 0 {
		body = wrapper.Output
	}
	var out struct {
		Telemetry json.RawMessage `json:"telemetry"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse render output: %w", err)
	}
	targets, err := telemetry.Decode(out.Telemetry)
	if err != nil {
		return nil, err
	}
	return telemetry.Normalize(targets), nil
}

// renderTelemetryEnvs renders every env under kclDir and decodes its
// telemetry. An env that fails to render is returned in unrendered.
func renderTelemetryEnvs(ctx context.Context, projectDir, kclDir string) (envs []telemetryEnv, unrendered []string) {
	entries, err := os.ReadDir(filepath.Join(projectDir, kclDir))
	if err != nil {
		return nil, nil
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		mainK := filepath.Join(projectDir, kclDir, ent.Name(), "main.k")
		if _, err := os.Stat(mainK); err != nil {
			continue
		}
		raw, err := lintKCLRun(ctx, projectDir, mainK, []string{
			"image_tag=" + strconv.Quote("lint"), "namespace=" + strconv.Quote(""), "env=" + strconv.Quote(ent.Name()),
		})
		if err != nil {
			unrendered = append(unrendered, ent.Name())
			continue
		}
		targets, err := decodeRenderedTelemetry(raw)
		if err != nil {
			unrendered = append(unrendered, ent.Name())
			continue
		}
		envs = append(envs, telemetryEnv{
			Name:    ent.Name(),
			File:    filepath.ToSlash(filepath.Join(kclDir, ent.Name(), "main.k")),
			Targets: targets,
		})
	}
	return envs, unrendered
}

// telemetryFindings runs the lane over every env of the project.
func telemetryFindings(ctx context.Context, projectDir string, cfg *config.ProjectConfig) telemetryLaneResult {
	kclDir := deployKCLDirFor(cfg)
	files := readKCLFiles(projectDir, kclDir)
	var res telemetryLaneResult
	if !telemetryAdopted(files) {
		return res
	}
	res.adopted = true
	envs, unrendered := renderTelemetryEnvs(ctx, projectDir, kclDir)
	res.unrendered = unrendered
	for _, e := range envs {
		if len(e.Targets) > 0 {
			res.envs++
			res.targets += len(e.Targets)
		}
	}
	all := checkTelemetry(envs, newTelemetryLocator(files))

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
	return res
}

// collectTelemetryJSON is the JSON arm. An error finding gates.
func collectTelemetryJSON(rc *lintRunCtx) ([]lintJSONFinding, bool, error) {
	res := telemetryFindings(rc.ctx, rc.cwd, rc.cfg)
	out := findingsToJSON(res.findings)
	switch {
	case !res.adopted:
		out = append(out, ranNote("no forge.Telemetry declared — nothing to check"))
	default:
		note := fmt.Sprintf("judged %d declared telemetry target(s) across %d env(s)", res.targets, res.envs)
		if len(res.unrendered) > 0 {
			note += fmt.Sprintf("; not judged, did not render: %s (`forge env render <env>` shows why)", strings.Join(res.unrendered, ", "))
		}
		out = append(out, ranNote(note))
	}
	return out, anyErrorFinding(out), nil
}
