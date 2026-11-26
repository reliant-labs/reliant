package cli

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// The Taskfile step of `forge fix tests-capture` (upgrade_tests_capture.go).
//
// Each test lane is recognised by the command an older forge scaffolded into
// it — compared as the decoded YAML string, so the single-quoted spelling of
// earlier releases and today's double-quoted one read the same — and
// rewritten to what the current template renders for it:
//
//   - the lane's command line is replaced by the template's
//     `forge test capture … -- go test …` line;
//   - the template's task-level `vars:` (RUN, and test:frontend's CAPTURE)
//     goes in above `cmds:`;
//   - test hands its RUN to test:frontend; test:frontend's call to
//     `frontends` gets LABEL and the capturing SCRIPT, and the `frontends`
//     task's `==>` line prints LABEL;
//   - a `test:report` task is added when there is none.
//
// Every inserted line is the rendered template's own line, so a project
// upgraded here reads as a new one does. Comments are left as they are. A
// lane is never touched unless it is exactly forge's old shape, laid out as
// forge laid it out, and the Taskfile declares every var the new lines use
// (FORGE, SLOT, GOTESTRACE): anything else is reported with the edit to make
// by hand. The edit is proven before it is written: the result must parse
// and decode to exactly the old document with those lanes replaced by the
// template's.

// testsCaptureTaskfilePlan is the Taskfile step's decision.
type testsCaptureTaskfilePlan struct {
	rel       string
	out       []byte // the edited Taskfile; nil when nothing changes
	rewrites  []string
	next      []string
	hasReport bool // test:report exists, or out adds it
	pending   bool // a lane is still in an old forge shape, or test:report is missing
}

// testsCaptureCLIArgs is how every lane takes `task test -- <args>`.
const testsCaptureCLIArgs = "{{if .CLI_ARGS}}{{.CLI_ARGS}}{{else}}./...{{end}}"

// testsCaptureGoLane is one Go lane's old scaffolded command: the decoded
// cmds entry an older forge wrote into task, and which template family's
// new line replaces it ("" is the Taskfile's own family).
type testsCaptureGoLane struct {
	task, lane, family string
	old                []string
}

// testsCaptureGoLanes are the old commands, from the git history of
// internal/templates/project/Taskfile{,.cli,.library}.yml.tmpl. Lanes
// scaffolded before forge had heavy slots lack the `{{.SLOT}} ` prefix (and
// the vars the new lines need, which the run then names).
var testsCaptureGoLanes = []testsCaptureGoLane{
	{"test", "unit", "", withAndWithoutSlot("go test {{.GOTESTRACE}} -count=1 -timeout 120s " + testsCaptureCLIArgs)},
	{"test:integration", "integration", "service", withAndWithoutSlot("go test {{.GOTESTRACE}} -tags=integration -count=1 -timeout=10m " + testsCaptureCLIArgs)},
	{"test:integration", "integration", "go", withAndWithoutSlot("go test {{.GOTESTRACE}} -count=1 -timeout 600s -tags=integration " + testsCaptureCLIArgs)},
	{"test:e2e", "e2e", "service", []string{"go test -tags=e2e -v -count=1 -timeout=20m {{if .CLI_ARGS}}{{.CLI_ARGS}}{{else}}./e2e/...{{end}}"}},
}

func withAndWithoutSlot(cmd string) []string { return []string{"{{.SLOT}} " + cmd, cmd} }

// testsCaptureOldFrontendScript is the SCRIPT test:frontend handed
// `frontends` from the release that resolved each frontend's package manager
// as $PM. Earlier frontend lanes (a hard-coded npm, an inline loop) run in a
// `frontends` task that exports no $PM, so they are not recognised.
const testsCaptureOldFrontendScript = `{{.SLOT}} $PM test $([ "$PM" != npm ] || printf %s --) --passWithNoTests`

// testsCaptureOldEcho is the `frontends` task's `==>` line before LABEL.
const testsCaptureOldEcho = `echo "==> {{.SCRIPT}} ($dir, $PM)"`

// testsCaptureTaskLocalVars are the vars the new lines use that the tasks
// themselves declare (or Task provides), so the Taskfile need not.
var testsCaptureTaskLocalVars = map[string]bool{"RUN": true, "CAPTURE": true, "LABEL": true, "SCRIPT": true, "CLI_ARGS": true}

var testsCaptureVarRef = regexp.MustCompile(`\{\{\s*\.([A-Z][A-Z0-9_]*)`)

// yamlText is a YAML file as its lines (endings stripped) and its top-level
// mapping node.
type yamlText struct {
	lines      []string
	nl         string
	trailingNL bool
	root       *yaml.Node
}

func parseYAMLText(src []byte) (*yamlText, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("does not parse as YAML (%v)", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("is not a YAML map")
	}
	s := string(src)
	y := &yamlText{nl: "\n", trailingNL: strings.HasSuffix(s, "\n"), root: doc.Content[0]}
	if strings.Contains(s, "\r\n") {
		y.nl = "\r\n"
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
	y.lines = strings.Split(s, "\n")
	for i := range y.lines {
		y.lines[i] = strings.TrimSuffix(y.lines[i], "\r")
	}
	return y, nil
}

// line is 1-based line n.
func (y *yamlText) line(n int) string { return y.lines[n-1] }

// render joins lines back into a file with y's line endings.
func (y *yamlText) render(lines []string) []byte {
	out := strings.Join(lines, y.nl)
	if y.trailingNL {
		out += y.nl
	}
	return []byte(out)
}

func yamlMapGet(m *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// taskfileTree is a Taskfile's text with its `tasks:` mapping.
type taskfileTree struct {
	*yamlText
	tasksKey, tasks *yaml.Node
}

func parseTaskfileTree(src []byte) (*taskfileTree, error) {
	y, err := parseYAMLText(src)
	if err != nil {
		return nil, err
	}
	t := &taskfileTree{yamlText: y}
	t.tasksKey, t.tasks = yamlMapGet(y.root, "tasks")
	return t, nil
}

func (t *taskfileTree) task(name string) (*yaml.Node, *yaml.Node) { return yamlMapGet(t.tasks, name) }

// taskEnd is the last line (1-based) of task name's own content: up to the
// next task's key, less the blank lines and key-level comments that belong
// to that key.
func (t *taskfileTree) taskEnd(name string) int {
	limit := len(t.lines) + 1
	var key *yaml.Node
	for i := 0; i+1 < len(t.tasks.Content); i += 2 {
		if key != nil {
			limit = t.tasks.Content[i].Line
			break
		}
		if t.tasks.Content[i].Value == name {
			key = t.tasks.Content[i]
		}
	}
	if key == nil {
		return 0
	}
	if limit == len(t.lines)+1 {
		for i := 0; i+1 < len(t.root.Content); i += 2 {
			if k := t.root.Content[i]; k.Line > t.tasksKey.Line && k.Line < limit {
				limit = k.Line
				break
			}
		}
	}
	end := limit - 1
	for end > key.Line {
		l := t.line(end)
		if s := strings.TrimSpace(l); s == "" || (strings.HasPrefix(s, "#") && indentOf(l) <= key.Column-1) {
			end--
			continue
		}
		break
	}
	return end
}

// cmdIndex finds the cmds entry of task v that match reports, with its index.
func cmdIndex(v *yaml.Node, match func(*yaml.Node) bool) (int, *yaml.Node) {
	_, cmds := yamlMapGet(v, "cmds")
	if cmds == nil || cmds.Kind != yaml.SequenceNode {
		return -1, nil
	}
	for i, it := range cmds.Content {
		if match(it) {
			return i, it
		}
	}
	return -1, nil
}

func isCaptureCmd(it *yaml.Node) bool {
	if it.Kind == yaml.MappingNode {
		_, it = yamlMapGet(it, "cmd")
	}
	return it != nil && it.Kind == yaml.ScalarNode && strings.Contains(it.Value, " test capture ")
}

func callsTask(target string) func(*yaml.Node) bool {
	return func(it *yaml.Node) bool {
		_, v := yamlMapGet(it, "task")
		return v != nil && v.Value == target
	}
}

// testsCaptureTemplate is one rendered Taskfile template, the source of every
// line the step writes.
type testsCaptureTemplate struct {
	*taskfileTree
	decoded map[string]any // the template's tasks, decoded
}

// varsLines are task name's `vars:` block in the template, through the line
// before `cmds:`.
func (tt *testsCaptureTemplate) varsLines(name string) []string {
	_, v := tt.task(name)
	vk, _ := yamlMapGet(v, "vars")
	ck, _ := yamlMapGet(v, "cmds")
	if vk == nil || ck == nil || ck.Line <= vk.Line {
		return nil
	}
	end := ck.Line - 1
	for end > vk.Line {
		if s := strings.TrimSpace(tt.line(end)); s == "" || (strings.HasPrefix(s, "#") && indentOf(tt.line(end)) == ck.Column-1) {
			end--
			continue
		}
		break
	}
	return append([]string(nil), tt.lines[vk.Line-1:end]...)
}

// captureLine is the template's `forge test capture` cmds line of task name,
// with its decoded value.
func (tt *testsCaptureTemplate) captureLine(name string) (string, string, bool) {
	_, v := tt.task(name)
	_, it := cmdIndex(v, isCaptureCmd)
	if it == nil || it.Kind != yaml.ScalarNode {
		return "", "", false
	}
	return tt.line(it.Line), it.Value, true
}

// callVars are the lines of the `vars:` that task name's call to target
// carries in the template, and that vars map decoded.
func (tt *testsCaptureTemplate) callVars(name, target string) ([]string, map[string]any) {
	_, v := tt.task(name)
	_, it := cmdIndex(v, callsTask(target))
	vk, vv := yamlMapGet(it, "vars")
	if vk == nil || vv.Kind != yaml.MappingNode || len(vv.Content) == 0 {
		return nil, nil
	}
	last := vv.Content[len(vv.Content)-1].Line
	var m map[string]any
	if err := vv.Decode(&m); err != nil {
		return nil, nil
	}
	return append([]string(nil), tt.lines[vk.Line-1:max(last, vk.Line)]...), m
}

func (tt *testsCaptureTemplate) taskDecoded(name string) map[string]any {
	m, _ := tt.decoded[name].(map[string]any)
	return m
}

// lineContaining is the first line of task name holding s.
func (tt *testsCaptureTemplate) lineContaining(name, s string) string {
	k, _ := tt.task(name)
	if k == nil {
		return ""
	}
	for n := k.Line; n <= tt.taskEnd(name); n++ {
		if strings.Contains(tt.line(n), s) {
			return tt.line(n)
		}
	}
	return ""
}

// precedingTask is the task the template declares right before name.
func (tt *testsCaptureTemplate) precedingTask(name string) string {
	for i := 2; i+1 < len(tt.tasks.Content); i += 2 {
		if tt.tasks.Content[i].Value == name {
			return tt.tasks.Content[i-2].Value
		}
	}
	return ""
}

// testsCaptureTemplates renders the service and the Go-only (cli; library
// renders the same lanes) Taskfile templates once per process.
var testsCaptureTemplates = sync.OnceValues(func() (map[string]*testsCaptureTemplate, error) {
	out := map[string]*testsCaptureTemplate{}
	for family, name := range map[string]string{"service": "Taskfile.yml.tmpl", "go": "Taskfile.cli.yml.tmpl"} {
		src, err := templates.ProjectTemplates().Render(name, map[string]any{"Name": "app", "ProjectName": "app", "FrontendName": ""})
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", name, err)
		}
		t, err := parseTaskfileTree(src)
		if err != nil || t.tasks == nil {
			return nil, fmt.Errorf("rendered %s has no tasks: %v", name, err)
		}
		var doc struct {
			Tasks map[string]any `yaml:"tasks"`
		}
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return nil, fmt.Errorf("decode rendered %s: %w", name, err)
		}
		out[family] = &testsCaptureTemplate{taskfileTree: t, decoded: doc.Tasks}
	}
	return out, nil
})

// testsCaptureEdit replaces del lines at 0-based index at with ins.
type testsCaptureEdit struct {
	at, del int
	ins     []string
}

// testsCapturePlanner collects one Taskfile's edits, and the decoded change
// each makes, as planTestsCaptureTaskfile walks its lanes.
type testsCapturePlanner struct {
	p       *testsCaptureTaskfilePlan
	t       *taskfileTree
	tmpls   map[string]*testsCaptureTemplate
	family  string // "service" or "go": which template the Taskfile came from
	topVars map[string]any
	edits   []testsCaptureEdit
	patches []func(tasks map[string]any)
}

// planTestsCaptureTaskfile decides the Taskfile step. nil when the project
// has no Taskfile, or one without forge's `test` task.
func planTestsCaptureTaskfile(dir string) (*testsCaptureTaskfilePlan, error) {
	rel, src, err := readTaskfile(dir)
	if err != nil || rel == "" {
		return nil, err
	}
	tmpls, err := testsCaptureTemplates()
	if err != nil {
		return nil, err
	}
	p := &testsCaptureTaskfilePlan{rel: rel}
	t, perr := parseTaskfileTree(src)
	if perr != nil {
		return nil, nil //nolint:nilerr // not a Taskfile this step can read; trimpath-default names it
	}
	if k, _ := t.task("test"); k == nil {
		return nil, nil
	}
	if t.tasks.Style&yaml.FlowStyle != 0 {
		p.pending = true
		p.next = append(p.next, fmt.Sprintf("%s writes `tasks:` in flow style, so forge left it alone. Make each test lane record as a new project's Taskfile does (`%s skill load %s`)", rel, Name(), testsCapturePlaybookSkill))
		return p, nil
	}
	pl := &testsCapturePlanner{p: p, t: t, tmpls: tmpls, family: "go"}
	if k, _ := t.task("test:frontend"); k != nil {
		pl.family = "service"
	}
	_ = t.root.Decode(&struct {
		Vars *map[string]any `yaml:"vars"`
	}{&pl.topVars})

	for _, name := range []string{"test", "test:integration", "test:e2e"} {
		pl.goLane(name)
	}
	pl.frontendLane()
	p.hasReport = t.has("test:report")
	if !p.hasReport {
		pl.report()
	}
	if len(pl.edits) == 0 {
		return p, nil
	}
	out := t.render(applyTestsCaptureEdits(t.lines, pl.edits))
	if why := verifyTestsCaptureTaskfile(src, out, pl.patches); why != "" {
		p.rewrites = nil
		p.hasReport = t.has("test:report")
		p.next = append(p.next, fmt.Sprintf("%s could not be edited without changing its meaning (%s), so forge left it alone. Make each test lane record as a new project's Taskfile does (`%s skill load %s`)",
			rel, why, Name(), testsCapturePlaybookSkill))
		return p, nil
	}
	p.out = out
	return p, nil
}

func (t *taskfileTree) has(name string) bool { k, _ := t.task(name); return k != nil }

// applyTestsCaptureEdits applies edits to lines, each index meaning a line
// of the original.
func applyTestsCaptureEdits(orig []string, edits []testsCaptureEdit) []string {
	lines := append([]string(nil), orig...)
	order := make([]int, len(edits))
	for i := range order {
		order[i] = i
	}
	// From the bottom up, so each index still means the original line; at
	// one index the edit planned first ends up first.
	sort.SliceStable(order, func(a, b int) bool {
		if edits[order[a]].at != edits[order[b]].at {
			return edits[order[a]].at > edits[order[b]].at
		}
		return order[a] > order[b]
	})
	for _, i := range order {
		e := edits[i]
		lines = append(lines[:e.at], append(append([]string(nil), e.ins...), lines[e.at+e.del:]...)...)
	}
	return lines
}

// missingVars are the vars lines use that the Taskfile's top-level `vars:`
// does not declare (and no task declares locally).
func (pl *testsCapturePlanner) missingVars(lines ...string) []string {
	var miss []string
	for _, m := range testsCaptureVarRef.FindAllStringSubmatch(strings.Join(lines, "\n"), -1) {
		if name := m[1]; !testsCaptureTaskLocalVars[name] && !containsString(miss, name) {
			if _, ok := pl.topVars[name]; !ok {
				miss = append(miss, name)
			}
		}
	}
	sort.Strings(miss)
	return miss
}

func (pl *testsCapturePlanner) missingVarsWhy(miss []string) string {
	return fmt.Sprintf("the Taskfile declares no top-level %s var (it predates them), and the new lines use them; add them to `vars:` as a new project's Taskfile declares them", strings.Join(miss, ", "))
}

// manual names an old lane forge could not edit, and why.
func (pl *testsCapturePlanner) manual(task, why string) {
	pl.p.pending = true
	pl.p.next = append(pl.p.next, fmt.Sprintf("%s: task %#q is forge's old test lane, but %s, so forge left it alone. Edit it by hand into the shape a new project's Taskfile has (`%s skill load %s`), or fix that and re-run `%s fix tests-capture`",
		pl.p.rel, task, why, Name(), testsCapturePlaybookSkill, Name()))
}

// varsAt is where a task's new `vars:` goes: above `cmds:` and the
// comments directly above it.
func (pl *testsCapturePlanner) varsAt(ck *yaml.Node) int {
	at := ck.Line - 1
	for at > 0 && strings.HasPrefix(strings.TrimSpace(pl.t.lines[at-1]), "#") && indentOf(pl.t.lines[at-1]) == ck.Column-1 {
		at--
	}
	return at
}

// laidOut reports whether the project's task and its `cmds:` sit where the
// template's do, so the template's lines drop in as they are.
func (pl *testsCapturePlanner) laidOut(tt *testsCaptureTemplate, name string, k, v *yaml.Node) bool {
	tk, tv := tt.task(name)
	ck, _ := yamlMapGet(v, "cmds")
	tck, _ := yamlMapGet(tv, "cmds")
	return tk != nil && ck != nil && tck != nil && k.Column == tk.Column && ck.Column == tck.Column && ck.Line > k.Line
}

// oldGoLane finds the cmds entry of task v that is an old scaffolded lane.
func oldGoLane(name string, v *yaml.Node) (int, *yaml.Node, *testsCaptureGoLane) {
	var shape *testsCaptureGoLane
	idx, item := cmdIndex(v, func(it *yaml.Node) bool {
		if it.Kind != yaml.ScalarNode {
			return false
		}
		for i := range testsCaptureGoLanes {
			if l := &testsCaptureGoLanes[i]; l.task == name && containsString(l.old, it.Value) {
				shape = l
				return true
			}
		}
		return false
	})
	return idx, item, shape
}

// goLane plans one Go lane: test, test:integration or test:e2e.
func (pl *testsCapturePlanner) goLane(name string) {
	t := pl.t
	k, v := t.task(name)
	if k == nil {
		return
	}
	idx, item, shape := oldGoLane(name, v)
	fam := pl.family
	if shape != nil && shape.family != "" {
		fam = shape.family
	}
	tt := pl.tmpls[fam]
	if _, _, ok := tt.captureLine(name); !ok {
		tt = pl.tmpls["service"]
	}
	newLine, newValue, _ := tt.captureLine(name)
	if shape == nil {
		if i, _ := cmdIndex(v, isCaptureCmd); i < 0 && v.Kind == yaml.MappingNode {
			lane := map[string]string{"test": "unit", "test:integration": "integration", "test:e2e": "e2e"}[name]
			pl.p.next = append(pl.p.next, fmt.Sprintf("%s: task %#q is not the shape forge scaffolded (it was edited), so forge left it alone. To record it as lane %s, give the task `vars: {RUN: {sh: \"{{.FORGE}} test begin\"}}` and run its `go test` through forge test capture with -json, as a new project's Taskfile does: %s (`%s skill load %s`)",
				pl.p.rel, name, lane, newValue, Name(), testsCapturePlaybookSkill))
		}
		return
	}
	pl.p.pending = true
	varsLines := tt.varsLines(name)
	vk, _ := yamlMapGet(v, "vars")
	switch {
	case v.Style&yaml.FlowStyle != 0:
		pl.manual(name, "it is written in flow style")
		return
	case vk != nil:
		pl.manual(name, "it already declares its own `vars:`")
		return
	case !pl.laidOut(tt, name, k, v) || item.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 ||
		!strings.HasPrefix(t.line(item.Line), leadingDash(newLine)):
		pl.manual(name, "it is indented or spelled differently from forge's layout")
		return
	case len(varsLines) == 0:
		pl.manual(name, "this forge's template has no `vars:` for it")
		return
	}
	callIdx, call, callLine, callVars := pl.frontendCall(tt, name, v)
	if miss := pl.missingVars(append(append([]string{newLine}, varsLines...), callLine)...); len(miss) > 0 {
		pl.manual(name, pl.missingVarsWhy(miss))
		return
	}
	ck, _ := yamlMapGet(v, "cmds")
	pl.edits = append(pl.edits,
		testsCaptureEdit{at: pl.varsAt(ck), ins: varsLines},
		testsCaptureEdit{at: item.Line - 1, del: 1, ins: []string{newLine}})
	tvars := tt.taskDecoded(name)["vars"]
	pl.patches = append(pl.patches, func(tasks map[string]any) {
		task := tasks[name].(map[string]any)
		task["vars"] = tvars
		task["cmds"].([]any)[idx] = newValue
	})
	what := "results and coverage"
	if name == "test:e2e" {
		what = "results"
	}
	if call != nil {
		pl.edits = append(pl.edits, testsCaptureEdit{at: call.Line, ins: []string{callLine}})
		pl.patches = append(pl.patches, func(tasks map[string]any) {
			tasks["test"].(map[string]any)["cmds"].([]any)[callIdx].(map[string]any)["vars"] = callVars
		})
		what += ", in one run with the frontend lanes"
	}
	pl.p.rewrites = append(pl.p.rewrites, fmt.Sprintf("%s: task %#q records lane %s (%s) through forge test capture", pl.p.rel, name, shape.lane, what))
}

// frontendCall is test's `- task: test:frontend` entry, when it is exactly
// forge's, with the `vars:` line the template gives it (handing down RUN).
// A nil call leaves test:frontend to open a run of its own.
func (pl *testsCapturePlanner) frontendCall(tt *testsCaptureTemplate, name string, v *yaml.Node) (int, *yaml.Node, string, map[string]any) {
	if name != "test" {
		return -1, nil, "", nil
	}
	lines, m := tt.callVars("test", "test:frontend")
	if len(lines) != 1 {
		return -1, nil, "", nil
	}
	idx, call := cmdIndex(v, callsTask("test:frontend"))
	_, tv := tt.task("test")
	_, tcall := cmdIndex(tv, callsTask("test:frontend"))
	if call == nil || len(call.Content) != 2 || tcall == nil || pl.t.line(call.Line) != tt.line(tcall.Line) {
		return -1, nil, "", nil
	}
	return idx, call, lines[0], m
}

// report adds test:report after the task the template puts it after.
func (pl *testsCapturePlanner) report() {
	t, p := pl.t, pl.p
	p.pending = true
	tt := pl.tmpls[pl.family]
	tk, _ := tt.task("test:report")
	after := tt.precedingTask("test:report")
	if !t.has(after) {
		after = "test"
	}
	at := t.taskEnd(after)
	block := append([]string{""}, tt.lines[tk.Line-1:tt.taskEnd("test:report")]...)
	if at < len(t.lines) && strings.TrimSpace(t.lines[at]) != "" {
		block = append(block, "")
	}
	if miss := pl.missingVars(block...); len(miss) > 0 {
		p.next = append(p.next, fmt.Sprintf("%s has no `test:report` task, and the Taskfile declares no top-level %s var for one; add the task by hand as a new project's Taskfile has it (`%s skill load %s`)",
			p.rel, strings.Join(miss, ", "), Name(), testsCapturePlaybookSkill))
		return
	}
	pl.edits = append(pl.edits, testsCaptureEdit{at: at, ins: block})
	report := tt.decoded["test:report"]
	pl.patches = append(pl.patches, func(tasks map[string]any) { tasks["test:report"] = report })
	p.rewrites = append(p.rewrites, fmt.Sprintf("%s: task `test:report` added after `%s:` (`task test:report` summarises the recorded runs; `-- --upload` sends them to the control plane)", p.rel, after))
	p.hasReport = true
}

// frontendLane plans test:frontend.
func (pl *testsCapturePlanner) frontendLane() {
	k, v := pl.t.task("test:frontend")
	if k == nil {
		return
	}
	edits, patch, msg, state := pl.frontendEdits(k, v)
	switch state {
	case "old":
		pl.p.pending = true
		pl.edits = append(pl.edits, edits...)
		pl.patches = append(pl.patches, patch)
		pl.p.rewrites = append(pl.p.rewrites, pl.p.rel+": "+msg)
	case "manual":
		pl.manual("test:frontend", msg)
	case "edited":
		pl.p.next = append(pl.p.next, fmt.Sprintf("%s: task `test:frontend` is not the shape forge scaffolded (it was edited), so forge left it alone. To record each Vitest frontend, give it the RUN and CAPTURE vars and the LABEL/SCRIPT it hands `frontends`, as a new project's Taskfile does (`%s skill load %s`)",
			pl.p.rel, Name(), testsCapturePlaybookSkill))
	}
}

// frontendEdits decides test:frontend: its state is "current", "old" (with
// the edits, the patch and the report line), "manual" (old, with why it
// cannot be edited) or "edited".
func (pl *testsCapturePlanner) frontendEdits(k, v *yaml.Node) (edits []testsCaptureEdit, patch func(map[string]any), msg, state string) {
	t, tt := pl.t, pl.tmpls["service"]
	_, taskVars := yamlMapGet(v, "vars")
	if _, capture := yamlMapGet(taskVars, "CAPTURE"); capture != nil {
		return nil, nil, "", "current"
	}
	idx, call := cmdIndex(v, callsTask("frontends"))
	cvk, cv := yamlMapGet(call, "vars")
	sk, script := yamlMapGet(cv, "SCRIPT")
	if script == nil {
		return nil, nil, "", "edited"
	}
	if strings.Contains(script.Value, "test capture") || strings.Contains(script.Value, "CAPTURE") {
		return nil, nil, "", "current"
	}
	if script.Value != testsCaptureOldFrontendScript || len(cv.Content) != 2 {
		return nil, nil, "", "edited"
	}
	varsLines := tt.varsLines("test:frontend")
	callLines, callVars := tt.callVars("test:frontend", "frontends")
	echo := tt.lineContaining("frontends", ".LABEL | default .SCRIPT")
	ck, _ := yamlMapGet(v, "cmds")
	switch {
	case v.Style&yaml.FlowStyle != 0:
		return nil, nil, "it is written in flow style", "manual"
	case taskVars != nil:
		return nil, nil, "it already declares its own `vars:`", "manual"
	case len(varsLines) == 0 || len(callLines) < 2:
		return nil, nil, "this forge's template has no capturing frontend lane", "manual"
	case !pl.laidOut(tt, "test:frontend", k, v):
		return nil, nil, "it is indented differently from forge's layout", "manual"
	}
	switch {
	case cv.Style&yaml.FlowStyle != 0 && cvk.Line == cv.Line && script.Line == cvk.Line && indentOf(t.line(cvk.Line)) == indentOf(callLines[0]):
		// vars: {SCRIPT: '…'}, as releases before v1.11.0 wrote it.
		edits = append(edits, testsCaptureEdit{at: cvk.Line - 1, del: 1, ins: callLines})
	case cv.Style&yaml.FlowStyle == 0 && sk.Line == script.Line && script.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 && indentOf(t.line(sk.Line)) == indentOf(callLines[1]):
		edits = append(edits, testsCaptureEdit{at: sk.Line - 1, del: 1, ins: callLines[1:]})
	default:
		return nil, nil, "its call to `frontends` is laid out differently from forge's", "manual"
	}
	if miss := pl.missingVars(append(varsLines, callLines...)...); len(miss) > 0 {
		return nil, nil, pl.missingVarsWhy(miss), "manual"
	}
	edits = append(edits, testsCaptureEdit{at: pl.varsAt(ck), ins: varsLines})
	newEcho := strings.TrimSpace(echo)
	echoed := false
	if fk, _ := t.task("frontends"); fk != nil && newEcho != "" {
		for n := fk.Line; n <= t.taskEnd("frontends"); n++ {
			if l := t.line(n); strings.TrimSpace(l) == testsCaptureOldEcho {
				edits = append(edits, testsCaptureEdit{at: n - 1, del: 1, ins: []string{l[:indentOf(l)] + newEcho}})
				echoed = true
				break
			}
		}
	}
	tvars := tt.taskDecoded("test:frontend")["vars"]
	patch = func(tasks map[string]any) {
		task := tasks["test:frontend"].(map[string]any)
		task["vars"] = tvars
		task["cmds"].([]any)[idx].(map[string]any)["vars"] = callVars
		if !echoed {
			return
		}
		for _, c := range tasks["frontends"].(map[string]any)["cmds"].([]any) {
			switch c := c.(type) {
			case map[string]any:
				if s, ok := c["cmd"].(string); ok {
					c["cmd"] = strings.Replace(s, testsCaptureOldEcho, newEcho, 1)
				}
			}
		}
	}
	return edits, patch, "task `test:frontend` records each Vitest frontend as lane frontend-<dir> (results, and coverage when @vitest/coverage-v8 is installed), in the run `task test` hands it", "old"
}

// leadingDash is a cmds line's indentation and its `- `.
func leadingDash(line string) string {
	return line[:indentOf(line)] + "- "
}

// verifyTestsCaptureTaskfile proves an edit: out must parse and decode to
// exactly src with every patch applied. It returns why not, "" when it does.
func verifyTestsCaptureTaskfile(src, out []byte, patches []func(map[string]any)) string {
	var after, want map[string]any
	if err := yaml.Unmarshal(out, &after); err != nil {
		return fmt.Sprintf("the result does not parse: %v", err)
	}
	if err := yaml.Unmarshal(src, &want); err != nil {
		return err.Error()
	}
	tasks, ok := want["tasks"].(map[string]any)
	if !ok {
		return "it has no tasks map"
	}
	for _, p := range patches {
		p(tasks)
	}
	if !reflect.DeepEqual(after, want) {
		return "the result decodes differently from the old file with the new lanes"
	}
	return ""
}
