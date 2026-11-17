package shellrun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_PipelineAndExpansion(t *testing.T) {
	dir := t.TempDir()
	var out, errb bytes.Buffer
	err := Run(context.Background(), `echo "hello $WHO" | { read a b; echo "$a-$b"; } && pwd`, Options{Dir: dir, Env: map[string]string{"WHO": "world"}, Stdout: &out, Stderr: &errb})
	if err != nil {
		t.Fatalf("run: %v (%s)", err, errb.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "hello-world") {
		t.Errorf("stdout = %q", got)
	}
}

func TestRun_NonzeroExit(t *testing.T) {
	err := Run(context.Background(), "true && exit 7", Options{})
	var ee ExitError
	if !errors.As(err, &ee) || ee.Code != 7 {
		t.Fatalf("want ExitError 7, got %v", err)
	}
}

func TestRun_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, "while true; do :; done", Options{}); err == nil {
		t.Fatal("want error on cancelled ctx")
	}
}

func TestPortableGrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: x\ndev_target: k3d\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "app", "gen"), 0o755)
	os.WriteFile(filepath.Join(dir, "app", "serve.go"), []byte("ic, err := activity.Interceptor(deps)\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "app", "gen", "x.go"), []byte("var only_in_gen = 1\n"), 0o644)
	cases := map[string]bool{
		`grep -q '^dev_target:' forge.yaml`:                      true,
		`grep -q '^prod_target:' forge.yaml`:                     false,
		`rg -q 'dev_target' .`:                                   true,
		`grep -q 'dev_target' .`:                                 false,
		`grep -qr 'dev_target' .`:                                true,
		`grep -qi 'DEV_TARGET' forge.yaml && test -f forge.yaml`: true,
		`grep -q 'nope' missing.yaml`:                            false,
		// File selection, as migration detections spell it: only Go
		// sources, never gen/ or vendor/.
		`grep -rqsE --include='*.go' 'activity\.Interceptor\(' .`:                   true,
		`grep -rqsE --include='*.ts' 'activity\.Interceptor\(' .`:                   false,
		`grep -rqsE --include='*.go' --exclude-dir=app 'activity\.Interceptor\(' .`: false,
		`grep -rqsE --include='*.go' --exclude-dir=gen 'activity\.Interceptor\(' .`: true,
		`grep -rqs --include='*.go' --exclude='serve.go' 'activity\.Interceptor' .`: false,
		`grep -rqs --include='*.go' 'only_in_gen' .`:                                true,
		`grep -rqs --include='*.go' --exclude-dir=gen 'only_in_gen' .`:              false,
		// Without -E a pattern is a BRE, as GNU grep reads it: ( ) ? + { } |
		// are literal. RE2 rejected the unbalanced paren, so a detection
		// spelled for grep (`connect\.NewError(`) silently never matched.
		`grep -rqs --include='*.go' 'activity\.Interceptor(deps)' .`: true,
		`grep -qs 'Interceptor(deps' app/serve.go`:                   true,
		`grep -qs 'err\|nope' app/serve.go`:                          true, // \| is BRE alternation
		`grep -qs 'ic?' app/serve.go`:                                false,
		`grep -qsE 'ic?, err' app/serve.go`:                          true,
		`rg -q 'Interceptor\(deps\)' app`:                            true, // rg is extended syntax
		// A missing operand is an error, but -q still exits 0 on a match
		// in another one, as GNU grep does.
		`grep -rqs 'only_in_gen' missing app`:  true,
		`grep -rqs 'nowhere' missing app`:      false,
		`grep -qs '^dev_target:' x forge.yaml`: true,
	}
	for script, want := range cases {
		got := Run(context.Background(), script, Options{Dir: dir, Commands: PortableCommands()}) == nil
		if got != want {
			t.Errorf("%q = %v, want %v", script, got, want)
		}
	}
}
