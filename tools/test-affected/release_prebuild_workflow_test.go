package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release.yml decisions the prebuild script cannot see: which dispatches the
// prepare job admits, and which jobs and steps run when prepare selected a prebuild
// (build-backend skipped) or did not. The job and step `if:` expressions are read
// from the workflow and evaluated here, so breaking a platform job's condition, or
// dropping the dispatch precondition, fails this test instead of a release.

type releaseWorkflow struct {
	Jobs map[string]*releaseJob `yaml:"jobs"`
}

type releaseJob struct {
	Needs       any               `yaml:"needs"`
	If          string            `yaml:"if"`
	Environment string            `yaml:"environment"`
	Outputs     map[string]string `yaml:"outputs"`
	Steps       []releaseStep     `yaml:"steps"`
}

type releaseStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]any    `yaml:"with"`
}

func (j *releaseJob) needs() []string {
	switch n := j.Needs.(type) {
	case string:
		return []string{n}
	case []any:
		var out []string
		for _, v := range n {
			out = append(out, fmt.Sprint(v))
		}
		return out
	}
	return nil
}

func loadReleaseWorkflow(t *testing.T) *releaseWorkflow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf releaseWorkflow
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatalf("parse release.yml: %v", err)
	}
	return &wf
}

func (wf *releaseWorkflow) job(t *testing.T, id string) *releaseJob {
	t.Helper()
	j := wf.Jobs[id]
	if j == nil {
		t.Fatalf("release.yml has no job %q", id)
	}
	return j
}

var releasePlatformJobs = map[string]string{"release-macos": "mac", "release-linux": "linux", "release-windows": "win"}

// TestReleasePrebuildDispatchIsAdmittedByScript: a prebuild dispatch runs the
// script's admit (proven by scripts/test-release-prebuild.sh to refuse a non-dry or
// package-version-mismatched prebuild) on the dispatch's own inputs, first, and
// only when prebuild_version is set, so an ordinary dispatch is unaffected.
func TestReleasePrebuildDispatchIsAdmittedByScript(t *testing.T) {
	prepare := loadReleaseWorkflow(t).job(t, "prepare")
	idx, admit := -1, releaseStep{}
	for i, s := range prepare.Steps {
		if s.Name == "Prebuild preconditions" {
			idx, admit = i, s
		}
	}
	if idx < 0 {
		t.Fatal(`prepare has no "Prebuild preconditions" step`)
	}
	if want := `bash scripts/release-prebuild.sh admit "$V" "$DRY" electron/package.json`; strings.TrimSpace(admit.Run) != want {
		t.Errorf("Prebuild preconditions runs %q, want %q", admit.Run, want)
	}
	if admit.Env["V"] != "${{ inputs.prebuild_version }}" || admit.Env["DRY"] != "${{ inputs.dry_run }}" {
		t.Errorf("Prebuild preconditions must pass the dispatch inputs: env %v", admit.Env)
	}
	for _, c := range []struct {
		version string
		want    bool
	}{{"v1.2.3", true}, {"", false}} {
		ctx := map[string]any{"inputs.prebuild_version": c.version}
		if got := evalStepIf(t, admit.If, ctx); got != c.want {
			t.Errorf("Prebuild preconditions with prebuild_version=%q runs=%v, want %v", c.version, got, c.want)
		}
	}
	// Before anything is built or uploaded: a refused dispatch produces nothing.
	for _, s := range prepare.Steps[:idx] {
		if s.Uses != "" && !strings.HasPrefix(s.Uses, "actions/checkout@") {
			t.Errorf("step %q (%s) runs before the prebuild preconditions", s.Name, s.Uses)
		}
	}
	if got := prepare.Outputs["prebuilt_run"]; got != "${{ steps.prebuilt.outputs.run }}" {
		t.Errorf("prepare.outputs.prebuilt_run = %q, want the prebuilt step's run", got)
	}
}

// TestReleaseJobsWithAndWithoutPrebuild evaluates the job and step conditions: with
// a selected prebuild, build-backend is skipped and every platform still runs and
// publishes the prebuild instead of building; without one, everything builds as
// before; a failed prepare runs no platform.
func TestReleaseJobsWithAndWithoutPrebuild(t *testing.T) {
	wf := loadReleaseWorkflow(t)
	backend := wf.job(t, "build-backend")
	for _, c := range []struct {
		name          string
		prepare       string
		prebuilt      string
		backendResult string // when build-backend runs
		wantBackend   bool
		wantPlatforms map[string]bool
	}{
		{"a tag with no prebuild builds everything", "success", "", "success", true,
			map[string]bool{"release-macos": true, "release-linux": true, "release-windows": true}},
		{"a tag with a prebuild skips the backend and still runs every platform", "success", "4242", "", false,
			map[string]bool{"release-macos": true, "release-linux": true, "release-windows": true}},
		{"a failed prepare runs nothing", "failure", "", "", false,
			map[string]bool{"release-macos": false, "release-linux": false, "release-windows": false}},
		{"a failed prepare runs nothing, even with a prebuild output", "failure", "4242", "", false,
			map[string]bool{"release-macos": false, "release-linux": false, "release-windows": false}},
		{"a failed backend build grounds Windows", "success", "", "failure", true,
			map[string]bool{"release-windows": false}},
	} {
		t.Run(c.name, func(t *testing.T) {
			results := map[string]string{"prepare": c.prepare}
			ctx := map[string]any{
				"needs.prepare.result":               c.prepare,
				"needs.prepare.outputs.prebuilt_run": c.prebuilt,
			}
			gotBackend := evalJobIf(t, backend, results, ctx)
			if gotBackend != c.wantBackend {
				t.Fatalf("build-backend runs=%v, want %v", gotBackend, c.wantBackend)
			}
			results["build-backend"] = "skipped"
			if gotBackend {
				results["build-backend"] = c.backendResult
			}
			ctx["needs.build-backend.result"] = results["build-backend"]
			for id, want := range c.wantPlatforms {
				j := wf.job(t, id)
				if got := evalJobIf(t, j, results, ctx); got != want {
					t.Errorf("%s runs=%v, want %v", id, got, want)
				}
				if want && c.prepare == "success" {
					checkPlatformSteps(t, id, j, ctx, c.prebuilt != "")
				}
			}
		})
	}
}

// checkPlatformSteps: a platform job either builds (electron-builder) or downloads
// the prebuild run's artifacts and publishes them through the script, never both.
func checkPlatformSteps(t *testing.T, id string, j *releaseJob, ctx map[string]any, prebuilt bool) {
	t.Helper()
	plat := releasePlatformJobs[id]
	var built, downloaded, published bool
	for _, s := range j.Steps {
		if !evalStepIf(t, s.If, ctx) {
			continue
		}
		switch {
		case strings.Contains(s.Run, "electron-builder"):
			built = true
		case strings.Contains(s.Run, "scripts/release-prebuild.sh publish "+plat+" "):
			published = true
		case strings.HasPrefix(s.Uses, "actions/download-artifact@") && s.With["run-id"] == "${{ needs.prepare.outputs.prebuilt_run }}":
			downloaded = true
		}
	}
	if built == prebuilt || downloaded != prebuilt || published != prebuilt {
		t.Errorf("%s with prebuilt=%v: built=%v downloaded-prebuild=%v published-prebuild=%v", id, prebuilt, built, downloaded, published)
	}
}

// evalJobIf applies GitHub's rule: a condition without a status function is
// ANDed with success() of the job's needs.
func evalJobIf(t *testing.T, j *releaseJob, results map[string]string, ctx map[string]any) bool {
	t.Helper()
	success := true
	for _, n := range j.needs() {
		r, ok := results[n]
		if !ok {
			t.Fatalf("no result for need %q", n)
		}
		success = success && r == "success"
	}
	return evalIf(t, j.If, ctx, success)
}

// evalStepIf treats the previous steps as successful.
func evalStepIf(t *testing.T, cond string, ctx map[string]any) bool {
	t.Helper()
	return evalIf(t, cond, ctx, true)
}

func evalIf(t *testing.T, cond string, ctx map[string]any, success bool) bool {
	t.Helper()
	expr := strings.TrimSpace(cond)
	if strings.HasPrefix(expr, "${{") && strings.HasSuffix(expr, "}}") {
		expr = strings.TrimSpace(expr[3 : len(expr)-2])
	}
	if expr == "" {
		return success
	}
	v, err := evalGHExpr(expr, ctx, success)
	if err != nil {
		t.Fatalf("evaluate %q: %v", expr, err)
	}
	b := truthy(v)
	if !strings.Contains(expr, "always()") && !strings.Contains(expr, "success()") &&
		!strings.Contains(expr, "failure()") && !strings.Contains(expr, "cancelled()") {
		b = b && success
	}
	return b
}

// evalGHExpr evaluates the subset of GitHub Actions expressions release.yml's
// conditions use: context paths, '...' strings, ==, !=, !, &&, ||, parentheses and
// always()/success(). A context path missing from ctx is an error, never ”.
func evalGHExpr(expr string, ctx map[string]any, success bool) (any, error) {
	p := &ghExprParser{src: expr, ctx: ctx, success: success}
	p.next()
	v, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.tok != "" {
		return nil, fmt.Errorf("unexpected %q", p.tok)
	}
	return v, nil
}

type ghExprParser struct {
	src     string
	pos     int
	tok     string
	ctx     map[string]any
	success bool
}

func (p *ghExprParser) next() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
	if p.pos >= len(p.src) {
		p.tok = ""
		return
	}
	s := p.src[p.pos:]
	for _, op := range []string{"&&", "||", "==", "!=", "!", "(", ")"} {
		if strings.HasPrefix(s, op) {
			p.tok, p.pos = op, p.pos+len(op)
			return
		}
	}
	if s[0] == '\'' {
		i := 1
		for i < len(s) {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i += 2
					continue
				}
				break
			}
			i++
		}
		p.tok, p.pos = s[:min(i+1, len(s))], p.pos+min(i+1, len(s))
		return
	}
	i := 0
	for i < len(s) && (s[i] == '_' || s[i] == '.' || s[i] == '-' ||
		(s[i] >= 'a' && s[i] <= 'z') || (s[i] >= 'A' && s[i] <= 'Z') || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		i = 1
	}
	p.tok, p.pos = s[:i], p.pos+i
}

func (p *ghExprParser) or() (any, error) {
	l, err := p.and()
	for err == nil && p.tok == "||" {
		p.next()
		var r any
		if r, err = p.and(); err == nil && !truthy(l) {
			l = r
		}
	}
	return l, err
}

func (p *ghExprParser) and() (any, error) {
	l, err := p.cmp()
	for err == nil && p.tok == "&&" {
		p.next()
		var r any
		if r, err = p.cmp(); err == nil && truthy(l) {
			l = r
		}
	}
	return l, err
}

func (p *ghExprParser) cmp() (any, error) {
	l, err := p.unary()
	if err != nil || (p.tok != "==" && p.tok != "!=") {
		return l, err
	}
	op := p.tok
	p.next()
	r, err := p.unary()
	if err != nil {
		return nil, err
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if !lok || !rok {
		return nil, fmt.Errorf("compare only strings, got %#v %s %#v", l, op, r)
	}
	eq := strings.EqualFold(ls, rs)
	return eq == (op == "=="), nil
}

func (p *ghExprParser) unary() (any, error) {
	switch tok := p.tok; {
	case tok == "!":
		p.next()
		v, err := p.unary()
		return !truthy(v), err
	case tok == "(":
		p.next()
		v, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.tok != ")" {
			return nil, fmt.Errorf("want ), got %q", p.tok)
		}
		p.next()
		return v, nil
	case strings.HasPrefix(tok, "'"):
		p.next()
		if len(tok) < 2 || !strings.HasSuffix(tok, "'") {
			return nil, fmt.Errorf("unterminated string %s", tok)
		}
		return strings.ReplaceAll(tok[1:len(tok)-1], "''", "'"), nil
	case tok == "always" || tok == "success":
		p.next()
		if p.tok != "(" {
			return nil, fmt.Errorf("want ( after %s", tok)
		}
		p.next()
		if p.tok != ")" {
			return nil, fmt.Errorf("%s() takes no arguments", tok)
		}
		p.next()
		return tok == "always" || p.success, nil
	case tok == "":
		return nil, fmt.Errorf("unexpected end")
	default:
		p.next()
		if p.tok == "(" {
			return nil, fmt.Errorf("function %s() is not supported", tok)
		}
		v, ok := p.ctx[tok]
		if !ok {
			return nil, fmt.Errorf("context %q is not in the scenario", tok)
		}
		return v, nil
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != ""
	}
	return v != nil
}

// TestGHExprEvaluator pins the evaluator the workflow tests rely on.
func TestGHExprEvaluator(t *testing.T) {
	ctx := map[string]any{"a": "x", "b": "", "needs.build-backend.result": "success"}
	for expr, want := range map[string]bool{
		"a == 'x'":                          true,
		"a != 'x'":                          false,
		"b == ''":                           true,
		"!b":                                true,
		"always() && b != ''":               false,
		"always() && (b != '' || a == 'X')": true,
		"needs.build-backend.result == 'success' && a == 'it''s'": false,
		"b || a":    true,
		"a && b":    false,
		"success()": false,
	} {
		v, err := evalGHExpr(expr, ctx, false)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if truthy(v) != want {
			t.Errorf("%s = %v, want %v", expr, v, want)
		}
	}
	for _, bad := range []string{"missing == ''", "startsWith(a, 'x')", "a == 'x", "(a == 'x'"} {
		if _, err := evalGHExpr(bad, ctx, true); err == nil {
			t.Errorf("%s: want an error", bad)
		}
	}
}
