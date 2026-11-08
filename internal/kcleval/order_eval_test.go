package kcleval_test

// order_eval_test.go pins that `forge kcl eval` keeps KCL's DECLARED key order
// and prints the same bytes `kcl run --format json` does.
//
// The defect: Eval decoded the render into a Go map, which has no order, so the
// JSON came back alphabetised. A consumer comparing committed JSON byte for
// byte (reliant's release-config drift check) could not use `forge kcl eval`
// alone and needed a second `kcl run -E forge=<module>` pass.

import (
	"os"
	"os/exec"
	"testing"

	"github.com/reliant-labs/forge/internal/kcleval"
)

// orderSource declares every key out of alphabetical order, at every level:
// top-level, a nested object, an object inside a list, and a list inside a
// list. `ratio` and `big` pin that a number prints as KCL wrote it.
const orderSource = `zeta = 1
alpha = {
    z = 1
    b = 2
    a = {
        y = 1
        c = 2
    }
}
items = [{q = 1, b = 2}, [3, {n = None, m = "x<&>"}]]
ratio = 1.0
big = 1e20
empty_list = []
empty_obj = {}
`

// orderGolden is `kcl run --format json` (kcl 0.13.1) of orderSource, captured
// verbatim: 4-space indent, KCL's number text, no HTML escaping, `[]` and `{}`
// for empty containers.
const orderGolden = `{
    "zeta": 1,
    "alpha": {
        "z": 1,
        "b": 2,
        "a": {
            "y": 1,
            "c": 2
        }
    },
    "items": [
        {
            "q": 1,
            "b": 2
        },
        [
            3,
            {
                "n": null,
                "m": "x<&>"
            }
        ]
    ],
    "ratio": 1.0,
    "big": 1e+20,
    "empty_list": [],
    "empty_obj": {}
}
`

const orderGoldenYAML = `zeta: 1
alpha:
  z: 1
  b: 2
  a:
    'y': 1
    c: 2
items:
- q: 1
  b: 2
- - 3
  - 'n': null
    m: x<&>
ratio: 1.0
big: 1e+20
empty_list: []
empty_obj: {}
`

func evalOrder(t *testing.T, format kcleval.Format) []byte {
	t.Helper()
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod": kclMod,
		"deploy/kcl/lib/o.k": orderSource,
	})
	res, err := kcleval.Eval(kcleval.Request{ProjectDir: dir, File: "deploy/kcl/lib/o.k"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := kcleval.Render(res, format)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestEvalJSONKeepsDeclaredOrder: the whole document renders in declared
// order, nested objects included, as the bytes `kcl run --format json` prints.
func TestEvalJSONKeepsDeclaredOrder(t *testing.T) {
	if got := string(evalOrder(t, kcleval.FormatJSON)); got != orderGolden {
		t.Errorf("--format json is not KCL's declared-order output.\ngot:\n%s\nwant:\n%s", got, orderGolden)
	}
}

// TestEvalYAMLKeepsDeclaredOrder: --format yaml keeps the same order.
func TestEvalYAMLKeepsDeclaredOrder(t *testing.T) {
	if got := string(evalOrder(t, kcleval.FormatYAML)); got != orderGoldenYAML {
		t.Errorf("--format yaml is not in declared order.\ngot:\n%s\nwant:\n%s", got, orderGoldenYAML)
	}
}

// TestEvalJSONMatchesKclRun compares against a real `kcl` when one is on PATH,
// so the golden above cannot drift from what kcl prints. Skipped without one:
// forge itself needs no kcl binary.
func TestEvalJSONMatchesKclRun(t *testing.T) {
	kcl, err := exec.LookPath("kcl")
	if err != nil {
		t.Skip("no kcl on PATH; orderGolden pins its output")
	}
	dir := project(t, map[string]string{
		"deploy/kcl/kcl.mod": kclMod,
		"deploy/kcl/lib/o.k": orderSource,
	})
	// A package home of its own. kcl prints "waiting for package-cache
	// lock..." on STDOUT while another kcl holds the shared one (~/.kcl), and
	// on a machine running other kcl processes that line led `want` and
	// failed the comparison.
	run := exec.Command(kcl, "run", dir+"/deploy/kcl/lib/o.k", "--format", "json")
	run.Env = append(os.Environ(), "KCL_PKG_PATH="+t.TempDir())
	want, err := run.Output()
	if err != nil {
		t.Skipf("kcl run failed (%v); orderGolden pins its output", err)
	}
	if got := evalOrder(t, kcleval.FormatJSON); string(got) != string(want) {
		t.Errorf("forge kcl eval differs from kcl run --format json.\ngot:\n%s\nwant:\n%s", got, want)
	}
}
