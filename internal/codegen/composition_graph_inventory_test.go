package codegen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeAppFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// DeclaredRoots is the inventory audit and shapes report: every //forge:root
// with its options and location, and the root-only providers that name it —
// read by the composition graph's own discovery, so it can never list a root
// generate does not see.
func TestDeclaredRoots(t *testing.T) {
	const roots = `package app

// Server serves the public API.
//
//forge:root server
type Server struct {
	API  *API
	Jobs *Jobs
}

// The background processor.
//
//forge:root background command=run,background migrate=wait adopt
type Background struct {
	Router *Router
}
`
	const providers = `package app

type RouterDeps struct{}

// NewRouter is the background process's privileged router.
//
//forge:root-only background
func NewRouter(RouterDeps) *Router { return nil }

type CacheDeps struct{}

//forge:root-only background,server
func NewCache(CacheDeps) *Cache { return nil }
`
	cases := []struct {
		name  string
		files map[string]string
		want  []DeclaredRoot
		err   string
	}{
		{
			name:  "no internal/app",
			files: map[string]string{"go.mod": "module x\n"},
		},
		{
			name:  "no root declared",
			files: map[string]string{"internal/app/providers.go": "package app\n"},
		},
		{
			name: "roots with options and root-only providers",
			files: map[string]string{
				"internal/app/roots.go":     roots,
				"internal/app/providers.go": providers,
				"internal/app/compose.go":   "package app\n\n//forge:root ignored\ntype Ignored struct{}\n",
			},
			want: []DeclaredRoot{
				{
					Name: "background", TypeName: "Background", Command: []string{"run", "background"},
					Migrate: "wait", Adopt: true, Short: "The background processor.", Fields: []string{"Router"},
					RootOnly: []RootOnlyProvider{
						{Name: "NewCache", File: "internal/app/providers.go", Line: 13},
						{Name: "NewRouter", File: "internal/app/providers.go", Line: 8},
					},
					File: "internal/app/roots.go", Line: 14,
				},
				{
					Name: "server", TypeName: "Server", Command: []string{"server"},
					Short: "Server serves the public API.", Fields: []string{"API", "Jobs"},
					RootOnly: []RootOnlyProvider{{Name: "NewCache", File: "internal/app/providers.go", Line: 13}},
					File:     "internal/app/roots.go", Line: 6,
				},
			},
		},
		{
			name:  "malformed directive",
			files: map[string]string{"internal/app/roots.go": "package app\n\n//forge:root server migrate=sometimes\ntype Server struct{}\n"},
			err:   "migrate=sometimes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DeclaredRoots(writeAppFiles(t, tc.files))
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one naming %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DeclaredRoots =\n%+v\nwant\n%+v", got, tc.want)
			}
		})
	}
}
