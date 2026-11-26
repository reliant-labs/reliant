package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

// `forge fix ci-oidc-permissions`: the mechanical half of the playbook
// migrations/v1.12.0/ci-oidc-uploads.
//
// From v1.12.0 `forge test report --upload` running in GitHub Actions needs
// no secret: it exchanges the job's GitHub OIDC token for a short-lived
// tests:write token (internal/cloud, ResolveCredentialFor). GitHub mints that
// token only for a job granted `id-token: write`, and a workflow's files are
// the project's own (scaffold-once), so a project scaffolded earlier, or one
// whose report step was added by hand or by `forge fix tests-capture`, still
// uploads only with FORGE_CONTROL_PLANE_TOKEN. This codemod grants it: each
// job of .github/workflows/*.yml whose steps run `forge test report` (or
// `task test:report`) gets `id-token: write`.
//
//   - A job with its own `permissions` mapping gets the one line added.
//   - A job with none inherits the workflow's, and a job-level block REPLACES
//     the workflow-level one, so it gets a block that repeats the workflow's
//     permissions plus `id-token: write`, with the comment the scaffold
//     carries. Placed where the scaffold places it (above `runs-on:`), an
//     older scaffolded ci.yml becomes the current template byte for byte.
//   - A job that already gets it (its own block, or a workflow-level one it
//     inherits, or `write-all`) is left alone.
//   - A job whose `id-token` is explicitly anything else, its own or
//     inherited, is never overridden: the run names it as a hand edit, and
//     the playbook stays pending until the person grants it or turns that
//     job's upload off.
//
// The file is edited as text at the positions the YAML parser reports, never
// re-marshalled, so every comment and quoting choice stays. The result must
// decode to the old workflow with exactly those jobs' permissions changed, or
// nothing is written. Re-running it changes nothing.

// ciOIDCRelease is the release the codemod and its playbook ship in.
const ciOIDCRelease = "v1.12.0"

// ciOIDCPlaybookID is the playbook's ID, as `forge project upgrade list`
// prints it.
const ciOIDCPlaybookID = ciOIDCRelease + "/ci-oidc-uploads"

// ciOIDCPlaybookSkill is the playbook's skill path.
const ciOIDCPlaybookSkill = "migrations/" + ciOIDCPlaybookID

// ciOIDCWorkflowsDir holds the workflows the codemod reads.
const ciOIDCWorkflowsDir = ".github/workflows"

func init() {
	codemod.Register(codemod.Codemod{
		Name:    "ci-oidc-permissions",
		Version: ciOIDCRelease,
		Short:   "Grant id-token: write to each GitHub Actions job that runs forge test report, so it uploads with OIDC and no secret",
		Long: `Give each job of .github/workflows/*.yml whose steps' ` + "`run:`" + ` scripts run
` + "`forge test report`" + ` (or ` + "`task test:report`" + `) without ` + "`--upload=false`" + ` the ` + "`id-token: write`" + ` permission. With it, forge
` + ciOIDCRelease + ` and later exchange the job's GitHub OIDC token for a short-lived
tests:write token, so the upload needs no FORGE_CONTROL_PLANE_TOKEN secret once
the Reliant GitHub App is linked to the organization.

A job with its own ` + "`permissions`" + ` gets the line added. A job with none gets a
job-level block repeating the workflow-level permissions plus ` + "`id-token: write`" + `,
because a job-level block replaces the workflow's. No other job changes, and
comments are kept: the file is edited in place, never re-written from a parse.
A job whose ` + "`id-token`" + ` is explicitly anything else, its own or inherited, is
never overridden: the run names it, and the playbook stays listed until you
grant it or pass ` + "`--upload=false`" + ` to that job's report. Re-running it changes
nothing.

The rest of the story (linking the App, retiring the secret): ` + "`forge skill load " + ciOIDCPlaybookSkill + "`" + `.`,
		Run: runCIOIDCPermissions,
	})
}

// ciOIDCPlan is one workflow's outcome: out is the edited file (nil when
// nothing changes), pending that a job still needs a hand edit.
type ciOIDCPlan struct {
	rel      string
	out      []byte
	rewrites []string
	next     []string
	pending  bool
}

func runCIOIDCPermissions(dir string, opts codemod.Options) (codemod.Report, error) {
	var rep codemod.Report
	plans, err := planCIOIDC(dir)
	if err != nil {
		return rep, err
	}
	for _, p := range plans {
		rep.Next = append(rep.Next, p.next...)
		if p.out == nil {
			continue
		}
		if opts.Write {
			if err := writeKeepingMode(filepath.Join(dir, filepath.FromSlash(p.rel)), p.out); err != nil {
				return rep, err
			}
		}
		rep.Rewrites = append(rep.Rewrites, p.rewrites...)
	}
	if rep.Changed() {
		rep.Verify = []string{"git diff -- " + ciOIDCWorkflowsDir}
	}
	return rep, nil
}

// planCIOIDC plans every workflow of the project, in file-name order.
func planCIOIDC(dir string) ([]ciOIDCPlan, error) {
	entries, err := os.ReadDir(filepath.Join(dir, filepath.FromSlash(ciOIDCWorkflowsDir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var plans []ciOIDCPlan
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".yml" && ext != ".yaml") {
			continue
		}
		rel := ciOIDCWorkflowsDir + "/" + e.Name()
		src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // the project's own workflow
		if err != nil {
			return nil, err
		}
		plans = append(plans, planCIOIDCWorkflow(rel, src))
	}
	return plans, nil
}

// projectNeedsCIOIDCPermissions is the playbook's detection (`forge-detect
// ci-oidc-permissions`): the codemod's own decision, so the playbook is
// offered exactly while a job that runs `forge test report` lacks `id-token:
// write` and has not declined it.
func projectNeedsCIOIDCPermissions(dir string) bool {
	plans, err := planCIOIDC(dir)
	if err != nil {
		return false
	}
	for _, p := range plans {
		if p.out != nil || p.pending {
			return true
		}
	}
	return false
}

// ciOIDCPermissionsComment is the comment the scaffold's ci.yml carries above
// a job-level permissions block, read from the rendered template so the
// codemod writes exactly what `forge project new` does.
var ciOIDCPermissionsComment = sync.OnceValues(func() ([]string, error) {
	src, err := templates.CITemplates("github").Render("ci.yml.tmpl", templates.CIWorkflowData{
		ProjectName: "app", TestRace: true, PermContents: "read", Module: "example.com/app",
	})
	if err != nil {
		return nil, fmt.Errorf("render ci.yml.tmpl: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "permissions:" || indentOf(l) == 0 {
			continue
		}
		start := i
		for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") && indentOf(lines[start-1]) == indentOf(l) {
			start--
		}
		out := make([]string, 0, i-start)
		for _, c := range lines[start:i] {
			out = append(out, strings.TrimSpace(c))
		}
		return out, nil
	}
	return nil, errors.New("ci.yml.tmpl has no job-level permissions block")
})

// ciOIDCJobEdit is what one job needs.
type ciOIDCJobEdit struct {
	at    int      // 0-based index the ins lines go before
	ins   []string // lines to insert
	line  int      // 0-based line replaced by repl, when repl != ""
	repl  string
	perms map[string]any // the job's permissions after the edit
	how   string         // the rewrite line's tail
}

// planCIOIDCWorkflow plans one workflow file.
func planCIOIDCWorkflow(rel string, src []byte) ciOIDCPlan {
	p := ciOIDCPlan{rel: rel}
	// A cheap pre-filter on the text; which jobs upload is decided from
	// their steps' `run:` scripts alone (ciOIDCJobUploads).
	if !ciOIDCNamesReport(string(src)) {
		return p
	}
	handEdit := func(why string) {
		p.pending = true
		p.next = append(p.next, fmt.Sprintf("%s, so forge left it alone. Give it `id-token: write` by hand (`%s skill load %s`)", why, Name(), ciOIDCPlaybookSkill))
	}
	y, err := parseYAMLText(src)
	if err != nil {
		handEdit(fmt.Sprintf("%s %v; each job in it that runs `forge test report` needs `id-token: write`", rel, err))
		return p
	}
	comment, err := ciOIDCPermissionsComment()
	if err != nil {
		handEdit(fmt.Sprintf("%s: %v", rel, err))
		return p
	}
	_, wfPerm := yamlMapGet(y.root, "permissions")
	_, jobs := yamlMapGet(y.root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return p
	}
	lines := append([]string(nil), y.lines...)
	changed := map[string]map[string]any{}
	var rewrites []string
	// Last job first: each edit leaves the lines of the jobs above it where
	// the parser saw them.
	for i := len(jobs.Content) - 2; i >= 0; i -= 2 {
		key, job := jobs.Content[i], jobs.Content[i+1]
		if job.Kind != yaml.MappingNode || !ciOIDCJobUploads(job) {
			continue
		}
		where := fmt.Sprintf("%s: job %s (line %d)", rel, key.Value, key.Line)
		e, skip, why, note := planCIOIDCJob(y, key, job, wfPerm, comment)
		if note != "" {
			p.next = append(p.next, where+" "+note)
		}
		if skip {
			continue
		}
		if why != "" {
			handEdit(where + " " + why)
			continue
		}
		if e.repl != "" {
			lines[e.line] = e.repl
		}
		if len(e.ins) > 0 {
			lines = append(lines[:e.at], append(append([]string(nil), e.ins...), lines[e.at:]...)...)
		}
		changed[key.Value] = e.perms
		rewrites = append([]string{where + ": `id-token: write` " + e.how}, rewrites...)
	}
	if len(changed) == 0 {
		return p
	}
	out := y.render(lines)
	if why := verifyCIOIDC(src, out, changed); why != "" {
		handEdit(fmt.Sprintf("%s could not be edited without changing its meaning (%s)", rel, why))
		return p
	}
	p.out = out
	p.rewrites = rewrites
	return p
}

// ciOIDCJobUploads reports whether one of the job's steps
// (jobs.<id>.steps[].run, nothing else in the file) runs the report without
// turning its upload off (ciOIDCRunUploads).
func ciOIDCJobUploads(job *yaml.Node) bool {
	_, steps := yamlMapGet(job, "steps")
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return false
	}
	for _, s := range steps.Content {
		_, run := yamlMapGet(s, "run")
		if run != nil && run.Kind == yaml.ScalarNode && ciOIDCRunUploads(run.Value) {
			return true
		}
	}
	return false
}

// ciOIDCIDToken reads `id-token` from a permissions node: its value, and
// whether that grants write. A `write-all` scalar grants it.
func ciOIDCIDToken(perm *yaml.Node) (value string, set bool) {
	if perm == nil {
		return "", false
	}
	if perm.Kind == yaml.ScalarNode && perm.Value == "write-all" {
		return "write", true
	}
	_, v := yamlMapGet(perm, "id-token")
	if v == nil {
		return "", false
	}
	return v.Value, true
}

// ciOIDCDeclined is the hand-edit reason for a job whose `id-token` is
// explicitly something other than write. forge never overrides that, and the
// playbook stays pending until the person decides: grant it, or stop the
// job uploading (--upload=false on its report step).
func ciOIDCDeclined(how, v string) string {
	return fmt.Sprintf("uploads test results but %s `id-token: %s`, which forge does not override: the playbook stays listed until the job gets `id-token: write` "+
		"(a FORGE_CONTROL_PLANE_TOKEN secret on the step still wins over OIDC) or its report command passes `--upload=false`", how, v)
}

// planCIOIDCJob decides one uploading job. skip: nothing to do (note may say
// why); why: a hand edit is needed; otherwise e is the edit.
func planCIOIDCJob(y *yamlText, key, job, wfPerm *yaml.Node, comment []string) (e ciOIDCJobEdit, skip bool, why, note string) {
	_, jp := yamlMapGet(job, "permissions")
	if jp == nil {
		// It inherits the workflow's permissions.
		if v, set := ciOIDCIDToken(wfPerm); set {
			if v != "write" {
				return e, false, ciOIDCDeclined("inherits the workflow's", v), ""
			}
			return e, true, "", ""
		}
		if job.Style&yaml.FlowStyle != 0 {
			return e, false, "is a flow mapping", ""
		}
		var entries [][2]string
		switch {
		case wfPerm == nil || (wfPerm.Kind == yaml.ScalarNode && wfPerm.Tag == "!!null"):
			entries = [][2]string{{"contents", "read"}}
			note = "ran with the repository's default GITHUB_TOKEN permissions (the workflow declares none). Its new block grants `contents: read` and `id-token: write` alone: add any other permission its steps need"
		case wfPerm.Kind == yaml.MappingNode:
			for j := 0; j+1 < len(wfPerm.Content); j += 2 {
				k, v := wfPerm.Content[j], wfPerm.Content[j+1]
				if k.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode {
					return e, false, "inherits workflow permissions forge cannot copy", ""
				}
				entries = append(entries, [2]string{k.Value, v.Value})
			}
		default:
			return e, false, fmt.Sprintf("inherits the workflow's `permissions: %s`, which a job-level block cannot extend: list the permissions its steps need plus `id-token: write`", wfPerm.Value), ""
		}
		entries = append(entries, [2]string{"id-token", "write"})
		child := job.Content[0].Column - 1
		step := child - (key.Column - 1)
		if step <= 0 {
			step = 2
		}
		// Above `runs-on:` (and the comments that lead it), where the
		// scaffold puts it; else first in the job.
		e.at = key.Line
		if rk, _ := yamlMapGet(job, "runs-on"); rk != nil && rk.Line > key.Line {
			e.at = rk.Line - 1
			for e.at > key.Line && strings.HasPrefix(strings.TrimSpace(y.lines[e.at-1]), "#") && indentOf(y.lines[e.at-1]) == child {
				e.at--
			}
		}
		pad := strings.Repeat(" ", child)
		for _, c := range comment {
			e.ins = append(e.ins, pad+c)
		}
		e.ins = append(e.ins, pad+"permissions:")
		e.perms = map[string]any{}
		for _, kv := range entries {
			e.ins = append(e.ins, pad+strings.Repeat(" ", step)+kv[0]+": "+kv[1])
			e.perms[kv[0]] = kv[1]
		}
		e.how = "in a new job-level block that repeats the workflow's permissions"
		return e, false, "", note
	}

	if v, set := ciOIDCIDToken(jp); set {
		if v != "write" {
			return e, false, ciOIDCDeclined("sets", v), ""
		}
		return e, true, "", ""
	}
	if jp.Kind != yaml.MappingNode {
		return e, false, fmt.Sprintf("has `permissions: %s`, which forge cannot extend: list the permissions its steps need plus `id-token: write`", jp.Value), ""
	}
	if err := jp.Decode(&e.perms); err != nil || e.perms == nil {
		e.perms = map[string]any{}
	}
	e.perms["id-token"] = "write"
	e.how = "added to its permissions"
	if jp.Style&yaml.FlowStyle != 0 {
		for _, n := range jp.Content {
			if n.Line != jp.Line {
				return e, false, "has a flow-style permissions mapping over several lines", ""
			}
		}
		line := y.line(jp.Line)
		closeAt := strings.LastIndex(line, "}")
		if closeAt < jp.Column-1 {
			return e, false, "has a flow-style permissions mapping forge cannot read", ""
		}
		before := strings.TrimRight(line[:closeAt], " ")
		sep := ", "
		if len(jp.Content) == 0 {
			sep = ""
		}
		e.line = jp.Line - 1
		e.repl = before + sep + "id-token: write" + line[len(before):]
		return e, false, "", ""
	}
	last := jp.Content[len(jp.Content)-1]
	e.at = last.Line
	e.ins = []string{strings.Repeat(" ", jp.Content[0].Column-1) + "id-token: write"}
	return e, false, "", ""
}

// verifyCIOIDC proves the edit: out decodes to src with exactly the changed
// jobs' permissions replaced.
func verifyCIOIDC(src, out []byte, changed map[string]map[string]any) string {
	var before, after map[string]any
	if err := yaml.Unmarshal(src, &before); err != nil {
		return err.Error()
	}
	if err := yaml.Unmarshal(out, &after); err != nil {
		return fmt.Sprintf("the result does not parse: %v", err)
	}
	jobs, _ := before["jobs"].(map[string]any)
	for name, perms := range changed {
		job, ok := jobs[name].(map[string]any)
		if !ok {
			return "job " + name + " is not a mapping"
		}
		job["permissions"] = perms
	}
	if !reflect.DeepEqual(before, after) {
		return "the workflow decodes differently afterwards"
	}
	return ""
}
