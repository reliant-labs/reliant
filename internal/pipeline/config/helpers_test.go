package config

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestParseSchedule(t *testing.T) {
	cases := []struct {
		in      string
		want    Schedule
		wantErr string
	}{
		{in: "0 6 * * *", want: Schedule{Cron: "0 6 * * *"}},
		{in: "  0   6 * *   1-5 ", want: Schedule{Cron: "0 6 * * 1-5"}},
		{in: "every 2h", want: Schedule{Every: 2 * time.Hour}},
		{in: "every 90m if main moved", want: Schedule{Every: 90 * time.Minute, IfMainMoved: true}},
		{in: "*/15 * * * * if main moved", want: Schedule{Cron: "*/15 * * * *", IfMainMoved: true}},
		{in: "every 1m", want: Schedule{Every: time.Minute}},
		{in: "", wantErr: "is empty"},
		{in: "every 59s", wantErr: "shortest interval is 1m"},
		{in: "every two hours", wantErr: "takes a duration"},
		{in: "0 6 * *", wantErr: "got 4 fields"},
		{in: "0 0 6 * * *", wantErr: "got 6 fields"},
		{in: "@daily", wantErr: "got 1 fields"},
		{in: "0 25 * * *", wantErr: "invalid cron line"},
	}
	for _, tc := range cases {
		got, err := ParseSchedule(tc.in)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ParseSchedule(%q) error = %v, want %q", tc.in, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("ParseSchedule(%q) = %+v, %v; want %+v", tc.in, got, err, tc.want)
			continue
		}
		// String renders the form ParseSchedule reads back.
		if again, err := ParseSchedule(got.String()); err != nil || again != got {
			t.Errorf("round trip of %q via %q = %+v, %v", tc.in, got.String(), again, err)
		}
	}
	if s := (Schedule{Every: 2 * time.Hour, IfMainMoved: true}).String(); s != DefaultReleaseSchedule {
		t.Errorf("String() = %q, want %q", s, DefaultReleaseSchedule)
	}
}

func TestDetectGate(t *testing.T) {
	taskfile := func(tasks ...string) *fstest.MapFile {
		var b strings.Builder
		b.WriteString("version: '3'\ntasks:\n")
		for _, task := range tasks {
			b.WriteString("  " + task + ":\n    cmds: [echo]\n")
		}
		return &fstest.MapFile{Data: []byte(b.String())}
	}
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	cases := []struct {
		name   string
		fs     fstest.MapFS
		want   string
		kind   RepoKind
		wantOK bool
	}{
		{"forge with ci:full", fstest.MapFS{"forge.yaml": file("name: x\n"), "Taskfile.yml": taskfile("test", "ci:full"), "go.mod": file("module x\n")}, "task ci:full", RepoForge, true},
		{"forge with ci:full in Taskfile.yaml", fstest.MapFS{"forge.yaml": file(""), "Taskfile.yaml": taskfile("\"ci:full\"")}, "task ci:full", RepoForge, true},
		{"forge without ci:full", fstest.MapFS{"forge.yaml": file(""), "Taskfile.yml": taskfile("test", "ci")}, "task test", RepoForge, true},
		{"forge without a Taskfile", fstest.MapFS{"forge.yaml": file("")}, "task test", RepoForge, true},
		{"forge with a broken Taskfile", fstest.MapFS{"forge.yaml": file(""), "Taskfile.yml": file("tasks: [")}, "task test", RepoForge, true},
		{"forge wins over package.json", fstest.MapFS{"forge.yaml": file(""), "package.json": file(`{"scripts":{"test":"vitest"}}`)}, "task test", RepoForge, true},
		{"node", fstest.MapFS{"package.json": file(`{"scripts":{"test":"vitest run"}}`)}, "npm test", RepoNode, true},
		{"node wins over go", fstest.MapFS{"package.json": file(`{"scripts":{"test":"jest"}}`), "go.mod": file("module x\n")}, "npm test", RepoNode, true},
		{"node placeholder test script is not a suite", fstest.MapFS{"package.json": file(`{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`)}, "", "", false},
		{"node without test script falls to go", fstest.MapFS{"package.json": file(`{"scripts":{"build":"tsc"}}`), "go.mod": file("module x\n")}, "go test ./...", RepoGo, true},
		{"broken package.json", fstest.MapFS{"package.json": file(`{`)}, "", "", false},
		{"go", fstest.MapFS{"go.mod": file("module x\n")}, "go test ./...", RepoGo, true},
		{"forge.yaml directory is not a forge project", fstest.MapFS{"forge.yaml/x": file("")}, "", "", false},
		{"nothing", fstest.MapFS{"README.md": file("hi")}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := DetectGate(tc.fs)
			if ok != tc.wantOK || p.Gate != tc.want || p.Kind != tc.kind {
				t.Fatalf("DetectGate = %+v, %v; want gate %q kind %q ok %v", p, ok, tc.want, tc.kind, tc.wantOK)
			}
			if !ok {
				return
			}
			if p.Reason == "" {
				t.Error("a proposal must say why")
			}
			// The proposed file is a valid pipeline.yaml with that gate.
			r, err := Load(p.File())
			if err != nil || r.Gate.Run != p.Gate {
				t.Fatalf("proposal file:\n%s\nLoad = %+v, %v", p.File(), r.Gate, err)
			}
		})
	}
}

func TestPinBumpCommand(t *testing.T) {
	pin := Pin{
		Repo:         "reliant-labs/reliant",
		LockstepWith: []string{"reliant-labs/forge"},
		Bump:         "task pin:forge REF={reliant-labs/forge} RELIANT={Reliant-Labs/Reliant} SHORT={forge} HOME=${HOME} {a,b}",
	}
	got, err := pin.BumpCommand(map[string]string{"reliant-labs/forge": "f00", "RELIANT-LABS/reliant": "beef"})
	if err != nil {
		t.Fatal(err)
	}
	want := "task pin:forge REF=f00 RELIANT=beef SHORT=f00 HOME=${HOME} {a,b}"
	if got != want {
		t.Errorf("BumpCommand = %q, want %q", got, want)
	}
	if _, err := pin.BumpCommand(map[string]string{"reliant-labs/forge": "f00"}); err == nil || !strings.Contains(err.Error(), "no sha given for reliant-labs/reliant") {
		t.Errorf("missing sha: err = %v", err)
	}
	if _, err := (Pin{Repo: "a/b"}).BumpCommand(nil); err == nil {
		t.Error("a pin with no bump must not produce a command")
	}
}

func TestConsumerCommandFor(t *testing.T) {
	c := Consumer{Command: "task ci:consumer PRODUCER_SHA={candidate} X=${candidate} Y={other}"}
	if got, want := c.CommandFor("abc"), "task ci:consumer PRODUCER_SHA=abc X=${candidate} Y={other}"; got != want {
		t.Errorf("CommandFor = %q, want %q", got, want)
	}
}

func TestParseInfraPatterns(t *testing.T) {
	res, err := ParseInfraPatterns([]byte("# infra\n\nno space left on device\n  ^dial tcp .*: i/o timeout$  \n"))
	if err != nil || len(res) != 2 || !res[1].MatchString("dial tcp 1.2.3.4:443: i/o timeout") {
		t.Fatalf("ParseInfraPatterns = %v, %v", res, err)
	}
	_, err = ParseInfraPatterns([]byte("ok\n(\nfine\n[\n"))
	errs, ok := err.(Errors)
	if !ok || len(errs) != 2 || errs[0].Line != 2 || errs[1].Line != 4 {
		t.Fatalf("ParseInfraPatterns(bad) = %v", err)
	}
}
