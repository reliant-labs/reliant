package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// The CI step of `forge fix tests-capture` (upgrade_tests_capture.go).
//
// The scaffolded .github/workflows/ci.yml runs `task test:report -- --upload`
// after its `task test` and `task test:integration` steps, so CI sends each
// run to the control plane. A ci.yml an older forge scaffolded has no such
// step, and the file is the project's own. This step inserts the template's
// step (and its comment), byte for byte, after each step whose `run:` is
// exactly `task test` or `task test:integration` (with the scaffold's
// optional ` GOTESTRACE=`), unless that job already runs test:report after
// it. The result must parse and decode to the old workflow plus those steps.
// Any other test step is the person's to follow, as the playbook shows.

// testsCaptureCIPath is the workflow the step edits.
const testsCaptureCIPath = ".github/workflows/ci.yml"

type testsCaptureCIPlan struct {
	out      []byte
	rewrites []string
	next     []string
}

var testsCaptureCITestRun = regexp.MustCompile(`^(\s*)(- )?run: task test(:integration)?( GOTESTRACE=)?\s*$`)

// testsCaptureCIStep is the rendered template's `Report test results` step:
// its lines (comment first), the indentation of its `- `, and the step
// decoded.
var testsCaptureCIStep = sync.OnceValues(func() (struct {
	lines  []string
	dash   int
	decode any
}, error,
) {
	var out struct {
		lines  []string
		dash   int
		decode any
	}
	src, err := templates.CITemplates("github").Render("ci.yml.tmpl", templates.CIWorkflowData{
		ProjectName: "app", TestRace: true, PermContents: "read", Module: "example.com/app",
	})
	if err != nil {
		return out, fmt.Errorf("render ci.yml.tmpl: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	at := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "- name: Report test results" {
			at = i
			break
		}
	}
	if at < 0 {
		return out, errors.New("ci.yml.tmpl has no `Report test results` step")
	}
	out.dash = indentOf(lines[at])
	start := at
	for start > 0 && strings.HasPrefix(strings.TrimSpace(lines[start-1]), "#") && indentOf(lines[start-1]) == out.dash {
		start--
	}
	end := at + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) > out.dash) {
		end++
	}
	for end > at+1 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	out.lines = lines[start:end]
	var steps []any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[at:end], "\n")), &steps); err != nil || len(steps) != 1 {
		return out, fmt.Errorf("decode the template's report step: %v", err)
	}
	out.decode = steps[0]
	return out, nil
})

// planTestsCaptureCI decides the CI step. nil when there is no ci.yml.
func planTestsCaptureCI(dir string) (*testsCaptureCIPlan, error) {
	src, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(testsCaptureCIPath))) //nolint:gosec // the project's own workflow
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	step, err := testsCaptureCIStep()
	if err != nil {
		return nil, err
	}
	p := &testsCaptureCIPlan{}
	y, perr := parseYAMLText(src)
	if perr != nil {
		p.next = append(p.next, fmt.Sprintf("%s %v, so forge left it alone. Add the `Report test results` step after your test steps by hand (`%s skill load %s`)",
			testsCaptureCIPath, perr, Name(), testsCapturePlaybookSkill))
		return p, nil
	}
	var edits []testsCaptureEdit
	for i, l := range y.lines {
		m := testsCaptureCITestRun.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		dash := len(m[1])
		if m[2] == "" {
			// The step's `- ` is on a line above, two columns left of `run:`.
			dash -= 2
			found := false
			for j := i - 1; j >= 0 && dash >= 0; j-- {
				lj := y.lines[j]
				if strings.TrimSpace(lj) == "" || indentOf(lj) > dash {
					continue
				}
				found = indentOf(lj) == dash && strings.HasPrefix(lj[dash:], "- ")
				break
			}
			if !found {
				continue
			}
		}
		end := i + 1 // 0-based index just past the step
		for end < len(y.lines) && (strings.TrimSpace(y.lines[end]) == "" || indentOf(y.lines[end]) > dash) {
			end++
		}
		for end > i+1 && strings.TrimSpace(y.lines[end-1]) == "" {
			end--
		}
		// Already reported: a later step of the same job runs test:report.
		reported := false
		for j := end; j < len(y.lines); j++ {
			lj := y.lines[j]
			if strings.TrimSpace(lj) != "" && indentOf(lj) < dash {
				break
			}
			if strings.Contains(lj, "task test:report") {
				reported = true
				break
			}
		}
		if reported {
			continue
		}
		ins := make([]string, len(step.lines))
		for k, sl := range step.lines {
			if strings.TrimSpace(sl) == "" {
				continue
			}
			ins[k] = strings.Repeat(" ", indentOf(sl)-step.dash+dash) + strings.TrimLeft(sl, " ")
		}
		edits = append(edits, testsCaptureEdit{at: end, ins: ins})
		p.rewrites = append(p.rewrites, fmt.Sprintf("%s: a `Report test results` step after %#q (line %d); it uploads with the FORGE_CONTROL_PLANE_TOKEN secret and never fails the job",
			testsCaptureCIPath, strings.TrimPrefix(strings.TrimSpace(l), "- "), i+1))
	}
	if len(edits) == 0 {
		if !strings.Contains(string(src), "task test:report") {
			p.next = append(p.next, fmt.Sprintf("%s has no step whose `run:` is `task test` or `task test:integration`, so forge added no report step. Add the `Report test results` step after your own test steps by hand (`%s skill load %s`)",
				testsCaptureCIPath, Name(), testsCapturePlaybookSkill))
		}
		return p, nil
	}
	lines := append([]string(nil), y.lines...)
	for k := len(edits) - 1; k >= 0; k-- {
		e := edits[k]
		lines = append(lines[:e.at], append(append([]string(nil), e.ins...), lines[e.at:]...)...)
	}
	out := y.render(lines)
	if why := verifyTestsCaptureCI(src, out, step.decode, len(edits)); why != "" {
		p.rewrites = nil
		p.next = append(p.next, fmt.Sprintf("%s could not be edited without changing its meaning (%s), so forge left it alone. Add the `Report test results` step after your test steps by hand (`%s skill load %s`)",
			testsCaptureCIPath, why, Name(), testsCapturePlaybookSkill))
		return p, nil
	}
	p.out = out
	return p, nil
}

// verifyTestsCaptureCI proves the edit: out parses, holds n more copies of
// the report step, and without every copy decodes exactly as src does.
func verifyTestsCaptureCI(src, out []byte, step any, n int) string {
	var before, after map[string]any
	if err := yaml.Unmarshal(src, &before); err != nil {
		return err.Error()
	}
	if err := yaml.Unmarshal(out, &after); err != nil {
		return fmt.Sprintf("the result does not parse: %v", err)
	}
	strip := func(doc map[string]any) int {
		removed := 0
		jobs, _ := doc["jobs"].(map[string]any)
		for _, j := range jobs {
			job, _ := j.(map[string]any)
			steps, ok := job["steps"].([]any)
			if !ok {
				continue
			}
			kept := steps[:0:0]
			for _, s := range steps {
				if reflect.DeepEqual(s, step) {
					removed++
					continue
				}
				kept = append(kept, s)
			}
			job["steps"] = kept
		}
		return removed
	}
	had, has := strip(before), strip(after)
	if has-had != n {
		return fmt.Sprintf("it holds %d new report steps, want %d", has-had, n)
	}
	if !reflect.DeepEqual(before, after) {
		return "the rest of the workflow decodes differently afterwards"
	}
	return ""
}
