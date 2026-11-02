package obsbackend

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

func TestDockerRunnerPassesSQLAndParamsAsPositionalArgs(t *testing.T) {
	var gotScript string
	var gotArgs []string
	r := DockerRunner{Exec: func(_ context.Context, script string, args []string) ([]byte, error) {
		gotScript, gotArgs = script, args
		return []byte(`{"meta":[{"name":"n","type":"UInt64"}],"data":[[3]],"rows":1}`), nil
	}}
	st := obsquery.Stmt{SQL: "SELECT count() AS n FROM default.otel_logs WHERE ServiceName = {p1:String}; DROP TABLE x", Params: map[string]string{"p2": "b", "p1": "it's \"x\" $(rm -rf /)"}}
	res, err := r.Run(context.Background(), st, Limits{Timeout: 5 * time.Second, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || AsInt(res.Rows[0][0]) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(gotScript, "DROP") || strings.Contains(gotScript, "rm -rf") {
		t.Errorf("the SQL or a value reached the shell script text: %s", gotScript)
	}
	if gotArgs[0] != st.SQL {
		t.Errorf("args[0] = %q, want the statement verbatim", gotArgs[0])
	}
	want := []string{"--param_p1=it's \"x\" $(rm -rf /)", "--param_p2=b"}
	if gotArgs[1] != want[0] || gotArgs[2] != want[1] {
		t.Errorf("params must follow in sorted order, got %q", gotArgs[1:3])
	}
	settings := strings.Join(gotArgs[3:], " ")
	for _, s := range []string{"--max_execution_time=5", "--max_result_rows=10", "--readonly=2", "--result_overflow_mode=throw"} {
		if !strings.Contains(settings, s) {
			t.Errorf("settings lack %s: %s", s, settings)
		}
	}
}

func TestLimitsBreakOverridesTheOverflowMode(t *testing.T) {
	s := Limits{Timeout: time.Second, MaxRows: 5, Break: true}.settings()
	if s["result_overflow_mode"] != "break" || s["readonly"] != 2 {
		t.Errorf("settings = %v", s)
	}
	if s := (Limits{Timeout: time.Second, MaxRows: 5}).settings(); s["result_overflow_mode"] != "throw" {
		t.Errorf("a structured query throws on overflow: %v", s)
	}
}

func TestDockerRunnerClassifiesFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		code   int
		has    string
		remedy string
	}{
		{"service not running", errors.New(`exit status 1: service "clickstack" is not running`), 2, "local ClickStack is not running", "forge env up dev"},
		{"no such service", errors.New("no such service: clickstack"), 2, "not running", "forge env up dev"},
		{"no such container", errors.New("exit status 1: Error response from daemon: No such container: x"), 2, "not running", "forge env up dev"},
		{"docker daemon down", errors.New("Cannot connect to the Docker daemon at unix:///var/run/docker.sock"), 2, "not running", "forge env up dev"},
		{"no docker", exec.ErrNotFound, 2, "docker is not installed", "forge env up dev"},
		{"clickhouse refused the statement", errors.New("exit status 62: Received exception from server (version 26.8.24):\nCode: 62. DB::Exception: Received from localhost:9000. DB::Exception: Syntax error: failed at position 8 (nope). (SYNTAX_ERROR)\n(query: select nope)"), 1, "Syntax error: failed at position 8", ""},
		{"unknown table", errors.New("Code: 60. DB::Exception: Table default.nothing does not exist. (UNKNOWN_TABLE)"), 1, "Table default.nothing does not exist", ""},
		{"something else", errors.New("exit status 137"), 2, "did not answer", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := DockerRunner{Env: "dev", Exec: func(context.Context, string, []string) ([]byte, error) { return nil, c.err }}
			_, err := r.Run(context.Background(), obsquery.Stmt{SQL: "select 1"}, DefaultLimits())
			var be *Error
			if !errors.As(err, &be) {
				t.Fatalf("want *Error, got %T %v", err, err)
			}
			if be.ExitCode() != c.code || !strings.Contains(be.Error(), c.has) {
				t.Errorf("code %d msg %q, want code %d containing %q", be.ExitCode(), be.Error(), c.code, c.has)
			}
			if c.remedy != "" && (len(be.Suggested) == 0 || be.Suggested[0] != c.remedy) {
				t.Errorf("remedy %v, want %q", be.Suggested, c.remedy)
			}
		})
	}
}

func TestDockerRunnerCancellationIsUndetermined(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := DockerRunner{Exec: func(ctx context.Context, _ string, _ []string) ([]byte, error) { return nil, ctx.Err() }}
	_, err := r.Run(ctx, obsquery.Stmt{SQL: "select 1"}, DefaultLimits())
	if err == nil || err.(*Error).ExitCode() != ExitUndetermined {
		t.Errorf("err = %v", err)
	}
}

func TestDecodeJSONCompactKeepsExactNumbersAndTypes(t *testing.T) {
	out := `{"meta":[{"name":"big","type":"UInt64"},{"name":"t","type":"DateTime64(9)"},{"name":"m","type":"Map(String, String)"},{"name":"a","type":"Array(String)"},{"name":"n","type":"Float64"}],
	"data":[[18446744073709551615,"2026-10-22 10:00:00.123456789",{"http.route":"/x"},["a","b"],null]],"rows":1}`
	r, err := DecodeJSONCompact([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	row := r.Rows[0]
	if AsUint(row[0]) != 18446744073709551615 {
		t.Errorf("a UInt64 must survive exactly, got %d", AsUint(row[0]))
	}
	if got := AsTime(row[1]); !got.Equal(time.Date(2026, 10, 22, 10, 0, 0, 123456789, time.UTC)) {
		t.Errorf("time = %s", got)
	}
	if AsStringMap(row[2])["http.route"] != "/x" || strings.Join(AsStrings(row[3]), ",") != "a,b" {
		t.Errorf("map/array: %v %v", row[2], row[3])
	}
	if AsFloat(row[4]) != 0 {
		t.Errorf("NaN arrives as null and reads as 0")
	}
	if r.Col("t") != 1 || r.Col("nope") != -1 {
		t.Errorf("Col lookup")
	}
	if empty, err := DecodeJSONCompact(nil); err != nil || len(empty.Rows) != 0 {
		t.Errorf("empty output is no rows: %v %v", empty, err)
	}
	if _, err := DecodeJSONCompact([]byte("not json")); err == nil || err.(*Error).ExitCode() != ExitUndetermined {
		t.Errorf("garbage output: %v", err)
	}
}

func TestComposeExecSelectsTheEnvsProject(t *testing.T) {
	var got []string
	compose := func(_ context.Context, dir string, args ...string) ([]byte, error) {
		got = append([]string{dir}, args...)
		return nil, nil
	}
	_, _ = ComposeExec(compose, "/proj", func(context.Context) []string { return []string{"-p", "forge-abc-dev"} })(context.Background(), "script", []string{"SELECT 1", "--param_p1=x"})
	want := "/proj -p forge-abc-dev exec -T clickstack sh -c script sh SELECT 1 --param_p1=x"
	if strings.Join(got, " ") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
}
