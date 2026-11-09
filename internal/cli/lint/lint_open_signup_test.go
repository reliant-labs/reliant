package lint

import (
	"os"
	"path/filepath"
	"testing"
)

func writeOpenSignupFile(t *testing.T, root, rel, src string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A broker scaffolded before v1.0.16 mounts sign-up unconditionally; one
// scaffolded since mounts it behind its switch. Only the first is flagged.
func TestOpenSignupFlagsOnlyAnUnconditionalMount(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
		want []int
	}{
		{
			name: "pre-v1.0.16 broker",
			src: `package app

const RegisterPath = "POST /auth/register"

func RegisterLoginBroker(mux *http.ServeMux) {
	mux.Handle(LoginPath, http.HandlerFunc(b.handleLogin))
	mux.Handle(RegisterPath, http.HandlerFunc(b.handleRegister))
}
`,
			want: []int{7},
		},
		{
			name: "gated broker",
			src: `package app

func RegisterLoginBroker(mux *http.ServeMux) {
	if broker.selfSignup {
		mux.Handle(RegisterPath, http.HandlerFunc(broker.handleRegister))
	}
}
`,
		},
		{
			name: "gated in an else branch",
			src: `package app

func RegisterLoginBroker(mux *http.ServeMux) {
	if inviteOnly {
		return
	} else {
		mux.HandleFunc(RegisterPath, broker.handleRegister)
	}
}
`,
		},
		{
			name: "literal pattern, unconditional",
			src: `package app

func mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/register", register)
	mux.HandleFunc("POST /auth/registered-devices", other)
}
`,
			want: []int{4},
		},
		{
			name: "route deleted (invite-only)",
			src: `package app

const RegisterPath = "POST /auth/register"

func RegisterLoginBroker(mux *http.ServeMux) {
	mux.Handle(LoginPath, http.HandlerFunc(b.handleLogin))
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeOpenSignupFile(t, root, "internal/app/login_broker.go", tc.src)
			// A test file and testdata are never the served broker.
			writeOpenSignupFile(t, root, "internal/app/login_broker_test.go", "package app\n\nfunc x() { mux.Handle(RegisterPath, h) }\n")
			writeOpenSignupFile(t, root, "internal/app/testdata/old.go", "package old\n\nfunc x() { mux.Handle(RegisterPath, h) }\n")

			got, err := findOpenSignupMounts(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("findings = %+v, want lines %v", got, tc.want)
			}
			for i, f := range got {
				if f.Path != "internal/app/login_broker.go" || f.Line != tc.want[i] {
					t.Errorf("finding %d = %+v, want internal/app/login_broker.go:%d", i, f, tc.want[i])
				}
			}
		})
	}
}

func TestOpenSignupFindingsAreAdvisoryAndPointAtTheFix(t *testing.T) {
	root := t.TempDir()
	writeOpenSignupFile(t, root, "internal/app/login_broker.go", "package app\n\nfunc r() { mux.Handle(RegisterPath, h) }\n")
	got, err := collectOpenSignupJSON(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want one", got)
	}
	f := got[0]
	if f.Rule != ruleOpenSignup || f.Severity != "warning" || f.File != "internal/app/login_broker.go" || f.Line != 3 {
		t.Errorf("finding = %+v", f)
	}
	if f.FixHint == "" {
		t.Error("the finding names no fix")
	}

	// A project with no internal/ tree is not an error.
	if got, err := collectOpenSignupJSON(t.TempDir()); err != nil || len(got) != 0 {
		t.Errorf("empty project: %+v, %v", got, err)
	}
}
