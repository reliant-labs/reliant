package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"

	"gopkg.in/yaml.v3"
)

// RepoKind is what DetectGate recognised a repository as.
type RepoKind string

const (
	RepoForge RepoKind = "forge"
	RepoNode  RepoKind = "node"
	RepoGo    RepoKind = "go"
)

// Proposal is a detected gate: a starting point the enrolment screen shows
// and the user confirms or edits before anything is written.
type Proposal struct {
	Kind RepoKind
	// Gate is the proposed gate command.
	Gate string
	// Reason says what in the tree led to it, for the enrolment screen.
	Reason string
}

// File renders the minimal pipeline.yaml for p, the file the enrolment
// screen proposes as a PR.
func (p Proposal) File() []byte {
	return fmt.Appendf(nil, "version: %d\ngate: %s # the full suite main must always pass\n", SupportedVersion, strconv.Quote(p.Gate))
}

// npmPlaceholderTest is the test script `npm init` writes, which fails by
// design: it is not a test suite.
const npmPlaceholderTest = `echo "Error: no test specified" && exit 1`

// taskfileNames are the root Taskfile names go-task reads, in its order.
var taskfileNames = []string{"Taskfile.yml", "taskfile.yml", "Taskfile.yaml", "taskfile.yaml"}

// DetectGate proposes a gate command from a repository's root, in this
// order:
//
//   - a forge project (forge.yaml): `task ci:full` when the root Taskfile
//     declares that task, else `task test`;
//   - a Node repository (package.json with a real `test` script): `npm test`;
//   - a Go repository (go.mod): `go test ./...`.
//
// It reads only the root of fsys (no includes, no workspaces) and reports
// false when nothing matches. Detection only proposes: the user confirms the
// command in the enrolment screen, because no tree can say which command is
// the full suite main must always pass.
func DetectGate(fsys fs.FS) (Proposal, bool) {
	if exists(fsys, "forge.yaml") {
		for _, name := range taskfileNames {
			if taskfileDeclares(fsys, name, "ci:full") {
				return Proposal{Kind: RepoForge, Gate: "task ci:full", Reason: "a forge project whose " + name + " declares ci:full, the whole gate"}, true
			}
		}
		return Proposal{Kind: RepoForge, Gate: "task test", Reason: "a forge project with no ci:full task"}, true
	}
	if data, err := fs.ReadFile(fsys, "package.json"); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(data, &pkg) == nil {
			if t := pkg.Scripts["test"]; t != "" && t != npmPlaceholderTest {
				return Proposal{Kind: RepoNode, Gate: "npm test", Reason: "package.json has a test script"}, true
			}
		}
	}
	if exists(fsys, "go.mod") {
		return Proposal{Kind: RepoGo, Gate: "go test ./...", Reason: "a Go module (go.mod)"}, true
	}
	return Proposal{}, false
}

func exists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

func taskfileDeclares(fsys fs.FS, name, task string) bool {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return false
	}
	var tf struct {
		Tasks map[string]yaml.Node `yaml:"tasks"`
	}
	if yaml.Unmarshal(data, &tf) != nil {
		return false
	}
	_, ok := tf.Tasks[task]
	return ok
}
