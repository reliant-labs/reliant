// Package testretry proves an ordinary Go test invocation recovered by
// running exactly its failed, unchanged, top-level tests once.
//
//forge:exclude-contract: a closed Go test protocol and a standalone CI operation, no service to substitute
package testretry

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

const (
	maxTests    = 5
	maxEvidence = 128 << 20
	maxLine     = 2 << 20
)

var (
	shaPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	packagePattern = regexp.MustCompile(`^(?:[A-Za-z0-9_][A-Za-z0-9_.-]*/)*[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	testPattern    = regexp.MustCompile(`^Test[A-Z0-9_][A-Za-z0-9_]*$`)
	faultPattern   = regexp.MustCompile(`(?im)WARNING: DATA RACE|race detected during execution of test|^\s*(panic:|fatal error:|SIG[A-Z]+:)|out of memory|no space left on device|too many open files|resource temporarily unavailable|signal: (killed|terminated|segmentation fault)`)
)

type identity struct{ pkg, name string }

// event is one line of `go test -json` (cmd/test2json) output; json.Unmarshal
// in scan fills it, and the tags name test2json's own field names.
type event struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

type execution struct {
	state  string
	active int
	ran    int
}

type failure struct {
	identity
	signature string
}

func scan(path string, visit func(event) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if !stat.Mode().IsRegular() || stat.Size() > maxEvidence {
		return errors.New("unsupported Go event stream")
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, maxEvidence+1), maxLine)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("incomplete or oversized Go event: %w", err)
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return fmt.Errorf("invalid Go event: %w", err)
		}
		if err := visit(ev); err != nil {
			return err
		}
	}
}

// judge interprets only Go's exact package/test identities and terminal
// events. Console line text is useful for rejecting faults, never for proving
// a pass. A partial stream or unaccounted package-level failure stays red.
func judge(path string, selected map[string]bool, retry *identity) ([]failure, error) {
	packages := make(map[string]*execution)
	tests := make(map[identity]string)
	output := make(map[identity]string)
	err := scan(path, func(ev event) error {
		if ev.Action == "build-fail" || ev.Action == "build-output" {
			return errors.New("build diagnostic in the Go stream")
		}
		if !selected[ev.Package] {
			return errors.New("event names an unselected package")
		}
		current := packages[ev.Package]
		if ev.Action == "start" {
			if current != nil || ev.Test != "" {
				return errors.New("duplicate or malformed package start")
			}
			packages[ev.Package] = new(execution)
			return nil
		}
		if current == nil || current.state != "" {
			return errors.New("event outside active package")
		}
		key := identity{ev.Package, ev.Test}
		if ev.Action == "output" {
			if faultPattern.MatchString(ev.Output) {
				return errors.New("panic, race, signal, or resource failure")
			}
			if tests[key] == "run" && testPattern.MatchString(ev.Test) && len(output[key]) < 3500 {
				output[key] += ev.Output[:min(len(ev.Output), 400)]
			}
			return nil
		}
		if ev.Test == "" {
			if current.active != 0 || !terminal(ev.Action) {
				return errors.New("package ended with incomplete tests")
			}
			current.state = ev.Action
			return nil
		}
		switch ev.Action {
		case "run":
			if tests[key] != "" || (retry != nil && key != *retry) {
				return errors.New("retry repeated or ran an unrelated test")
			}
			tests[key] = "run"
			current.active++
			current.ran++
		case "pause", "cont":
			if tests[key] != "run" {
				return errors.New("invalid test schedule event")
			}
		case "pass", "skip", "fail":
			if tests[key] != "run" {
				return errors.New("test terminal without a run")
			}
			tests[key] = ev.Action
			current.active--
		default:
			return errors.New("unrecognized Go test action")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(packages) != len(selected) {
		return nil, errors.New("incomplete package evidence")
	}
	red := make(map[string]bool)
	for pkg, phase := range packages {
		if phase.state == "" {
			return nil, errors.New("missing package verdict")
		}
		if phase.state == "fail" {
			red[pkg] = true
		}
	}
	if retry != nil {
		if len(tests) != 1 || tests[*retry] != "pass" || packages[retry.pkg].state != "pass" {
			return nil, errors.New("exact retry did not run once and pass")
		}
		return nil, nil
	}
	var failed []failure
	for key, state := range tests {
		if state != "fail" {
			continue
		}
		if !testPattern.MatchString(key.name) || !red[key.pkg] || strings.TrimSpace(output[key]) == "" {
			return nil, errors.New("failed test is not an attributable top-level Go test")
		}
		// A singleton retry cannot preserve siblings or subtests, even
		// ones that eventually skipped after beginning execution.
		if packages[key.pkg].ran != 1 {
			return nil, errors.New("original test selection would change on retry")
		}
		failed = append(failed, failure{key, strings.ToValidUTF8(output[key], "")})
	}
	if len(failed) == 0 || len(failed) > maxTests {
		return nil, errors.New("no isolated failing tests or too many failures")
	}
	for _, item := range failed {
		delete(red, item.pkg)
	}
	if len(red) != 0 {
		return nil, errors.New("package failure has no test cause")
	}
	slices.SortFunc(failed, func(a, b failure) int {
		if order := strings.Compare(a.pkg, b.pkg); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	return failed, nil
}

func terminal(action string) bool {
	return action == "pass" || action == "fail" || action == "skip"
}

func packageSet(module string, args []string) (map[string]bool, error) {
	selected := make(map[string]bool, len(args))
	for _, arg := range args {
		name := arg
		switch {
		case arg == ".":
			name = module
		case strings.HasPrefix(arg, "./"):
			name = module + arg[1:]
		}
		if !packagePattern.MatchString(name) || (name != module && !strings.HasPrefix(name, module+"/")) || selected[name] {
			return nil, fmt.Errorf("not a unique exact package in the module: %q", arg)
		}
		for piece := range strings.SplitSeq(name, "/") {
			if piece == "." || piece == ".." || strings.Contains(piece, "...") {
				return nil, errors.New("package pattern, not an exact package")
			}
		}
		selected[name] = true
	}
	if len(selected) == 0 {
		return nil, errors.New("no ordinary short packages supplied")
	}
	return selected, nil
}
