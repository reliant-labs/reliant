// Copyright (c) 2026 Reliant Labs
package machinepreflight

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

type fakeInfo struct {
	mode fs.FileMode
	uid  int
}

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// healthy is a machine every default check passes on: 100 GiB free on the
// home and /tmp, /tmp 1777 and writable, uid 1000 owning its home, git on
// PATH, load 1.0 on 4 CPUs.
func healthy() Env {
	return Env{
		Home:   "/home/workspace",
		StatFS: func(string) (Disk, error) { return Disk{FreeBytes: 100 << 30, TotalBytes: 200 << 30}, nil },
		Stat: func(path string) (fs.FileInfo, error) {
			if path == TmpDir {
				return fakeInfo{mode: fs.ModeDir | fs.ModeSticky | 0o777}, nil
			}
			return fakeInfo{mode: fs.ModeDir | 0o755, uid: 1000}, nil
		},
		Owner:      func(fi fs.FileInfo) (int, bool) { return fi.(fakeInfo).uid, true },
		CreateTemp: func(string) error { return nil },
		UID:        func() (int, bool) { return 1000, true },
		LookPath: func(name string) (string, error) {
			if name == "git" {
				return "/usr/bin/git", nil
			}
			return "", errors.New("not found")
		},
		Load5: func() (float64, bool) { return 4, true },
		CPUs:  4,
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name   string
		env    func(Env) Env
		spec   Spec
		reason Reason
		msg    string
	}{
		{name: "a healthy machine passes", env: func(e Env) Env { return e }},
		{
			name: "low disk on the home fails",
			env: func(e Env) Env {
				e.StatFS = func(path string) (Disk, error) {
					if path == "/home/workspace" {
						return Disk{FreeBytes: 1 << 30, TotalBytes: 200 << 30}, nil
					}
					return Disk{FreeBytes: 100 << 30, TotalBytes: 200 << 30}, nil
				}
				return e
			},
			reason: ReasonDiskLow, msg: "/home/workspace has 1 GiB free, under the 5 GiB the node needs",
		},
		{
			name: "low disk on /tmp fails against a raised floor",
			env: func(e Env) Env {
				e.StatFS = func(path string) (Disk, error) {
					if path == TmpDir {
						return Disk{FreeBytes: 8 << 30, TotalBytes: 200 << 30}, nil
					}
					return Disk{FreeBytes: 100 << 30, TotalBytes: 200 << 30}, nil
				}
				return e
			},
			spec:   Spec{MinFreeBytes: 10 << 30},
			reason: ReasonDiskLow, msg: "/tmp has 8 GiB free, under the 10 GiB",
		},
		{
			name: "a disk guard refusing heavy work fails",
			env: func(e Env) Env {
				e.DiskRefusal = func() string { return "the disk is 96% full\nfree some space" }
				return e
			},
			reason: ReasonDiskRefusing, msg: "refuses heavy work: the disk is 96% full",
		},
		{
			name: "/tmp without its sticky bit fails",
			env: func(e Env) Env {
				stat := e.Stat
				e.Stat = func(path string) (fs.FileInfo, error) {
					if path == TmpDir {
						return fakeInfo{mode: fs.ModeDir | 0o755}, nil
					}
					return stat(path)
				}
				return e
			},
			reason: ReasonTmpUnusable, msg: "/tmp has mode 0755, not 1777",
		},
		{
			name: "a missing /tmp fails",
			env: func(e Env) Env {
				stat := e.Stat
				e.Stat = func(path string) (fs.FileInfo, error) {
					if path == TmpDir {
						return nil, &fs.PathError{Op: "stat", Path: TmpDir, Err: fs.ErrNotExist}
					}
					return stat(path)
				}
				return e
			},
			reason: ReasonTmpUnusable, msg: "/tmp cannot be read: file does not exist",
		},
		{
			name: "an unwritable /tmp fails",
			env: func(e Env) Env {
				e.CreateTemp = func(string) error { return errors.New("read-only file system") }
				return e
			},
			reason: ReasonTmpUnusable, msg: "/tmp is not writable: read-only file system",
		},
		{
			name: "root fails",
			env: func(e Env) Env {
				e.UID = func() (int, bool) { return 0, true }
				e.Owner = func(fs.FileInfo) (int, bool) { return 0, true }
				return e
			},
			reason: ReasonUIDRoot,
		},
		{
			name: "root passes when allowed",
			env: func(e Env) Env {
				e.UID = func() (int, bool) { return 0, true }
				e.Owner = func(fs.FileInfo) (int, bool) { return 0, true }
				return e
			},
			spec: Spec{AllowRoot: true},
		},
		{
			name:   "a daemon not owning its home fails",
			env:    func(e Env) Env { e.UID = func() (int, bool) { return 1001, true }; return e },
			reason: ReasonUIDMismatch, msg: "runs as uid 1001 but its home /home/workspace belongs to uid 1000",
		},
		{
			name:   "git missing fails",
			env:    func(e Env) Env { e.LookPath = func(string) (string, error) { return "", errors.New("no") }; return e },
			reason: ReasonToolMissing, msg: "not on the daemon's PATH: git",
		},
		{
			name:   "a node's tool missing fails",
			spec:   Spec{Tools: []string{"task", "go", "task"}},
			env:    func(e Env) Env { return e },
			reason: ReasonToolMissing, msg: "not on the daemon's PATH: go, task",
		},
		{
			name:   "high load per CPU fails",
			env:    func(e Env) Env { e.Load5 = func() (float64, bool) { return 12, true }; return e },
			reason: ReasonLoadHigh, msg: "load is 3.00 per CPU",
		},
		{
			name: "high load passes under a raised threshold",
			env:  func(e Env) Env { e.Load5 = func() (float64, bool) { return 12, true }; return e },
			spec: Spec{MaxLoadPerCPU: 4},
		},
		{
			name: "readings the platform cannot give are skipped",
			env: func(Env) Env {
				return Env{LookPath: func(string) (string, error) { return "/usr/bin/git", nil }}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Check(tt.env(healthy()), tt.spec)
			if tt.reason == "" {
				if !res.OK || len(res.Failures) != 0 {
					t.Fatalf("got %+v, want it to pass", res)
				}
				return
			}
			if res.OK || res.Primary().Reason != tt.reason {
				t.Fatalf("got %+v, want %s", res, tt.reason)
			}
			if !strings.Contains(res.Primary().Message, tt.msg) {
				t.Fatalf("message %q, want it to contain %q", res.Primary().Message, tt.msg)
			}
		})
	}
}

// Every failing check is reported, in check order.
func TestCheck_ReportsEveryFailure(t *testing.T) {
	env := healthy()
	env.DiskRefusal = func() string { return "full" }
	env.Load5 = func() (float64, bool) { return 100, true }
	res := Check(env, Spec{})
	var got []Reason
	for _, f := range res.Failures {
		got = append(got, f.Reason)
	}
	if len(got) != 2 || got[0] != ReasonDiskRefusing || got[1] != ReasonLoadHigh {
		t.Fatalf("failures %v, want disk_refusing then load_high", got)
	}
	if !strings.Contains(res.Message(), "; ") {
		t.Fatalf("message %q joins nothing", res.Message())
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]uint64{
		"5GiB": 5 << 30, "5G": 5 << 30, "500MB": 500e6, "10 gib": 10 << 30, "1.5GiB": 3 << 29, "1048576": 1 << 20, "2TiB": 2 << 40,
	} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "GiB", "5XB", "-1G", "five"} {
		if _, err := ParseSize(in); err == nil {
			t.Errorf("ParseSize(%q) succeeded", in)
		}
	}
}

func TestFromProto(t *testing.T) {
	if _, ok, err := FromProto(nil); ok || err != nil {
		t.Fatalf("nil = ok %v, %v; want none", ok, err)
	}
	cfg, ok, err := FromProto(&reliantv1.NodePreflight{})
	if !ok || err != nil {
		t.Fatalf("empty = ok %v, %v", ok, err)
	}
	if cfg.MaxMachines != DefaultMaxMachines || cfg.Spec.MinFreeBytes != DefaultMinFreeBytes ||
		cfg.Spec.MaxLoadPerCPU != DefaultMaxLoadPerCPU || strings.Join(cfg.Spec.Tools, ",") != "git" {
		t.Fatalf("defaults = %+v", cfg)
	}
	cfg, _, err = FromProto(&reliantv1.NodePreflight{Tools: []string{"task"}, MinFreeDisk: "10GiB", MaxLoad: 3, AllowRoot: true, MaxMachines: 5})
	if err != nil || cfg.MaxMachines != 5 || cfg.Spec.MinFreeBytes != 10<<30 || cfg.Spec.MaxLoadPerCPU != 3 ||
		!cfg.Spec.AllowRoot || strings.Join(cfg.Spec.Tools, ",") != "git,task" {
		t.Fatalf("overrides = %+v, %v", cfg, err)
	}
	for _, bad := range []*reliantv1.NodePreflight{
		{MinFreeDisk: "lots"}, {MinFreeDisk: "0"}, {MaxLoad: -1}, {MaxMachines: 21}, {MaxMachines: -1}, {Tools: []string{"/usr/bin/go"}}, {Tools: []string{""}},
	} {
		if _, _, err := FromProto(bad); err == nil {
			t.Errorf("FromProto(%v) succeeded", bad)
		}
	}
}
