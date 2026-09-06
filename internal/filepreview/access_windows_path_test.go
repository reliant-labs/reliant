package filepreview

import "testing"

// A workspace on a Windows daemon must confine exactly like a POSIX one.
//
// Before the ospath fix, filepath.Abs on a Linux server prepended its OWN
// working directory to the base (`C:\Users\sean\proj` became
// `/cwd/C:\Users\sean\proj`) and filepath.IsAbs judged the requested path
// relative, so a legitimate in-workspace file was joined onto that mangled
// base instead of being returned as itself. Every read and write routed to a
// Windows daemon resolved to a path that exists nowhere.
func TestValidatePathScopedWindowsWorkspace(t *testing.T) {
	const base = `C:\Users\sean\src\proj`

	tests := []struct {
		name      string
		requested string
		want      string
		wantErr   error
	}{
		{"relative descendant", `src\main.go`, `C:\Users\sean\src\proj\src\main.go`, nil},
		{"relative descendant forward slash", "src/main.go", `C:\Users\sean\src\proj\src\main.go`, nil},
		{"absolute inside base", `C:\Users\sean\src\proj\src\main.go`, `C:\Users\sean\src\proj\src\main.go`, nil},
		{"base itself", base, base, nil},
		{"empty means workspace root", "", base, nil},
		{"slash means workspace root", "/", base, nil},

		{"relative traversal refused", `..\secret.txt`, "", ErrPathOutsideBase},
		{"absolute outside base refused", `C:\Windows\System32\config`, "", ErrAbsolutePathOutsideBase},
		{"sibling prefix is not inside", `C:\Users\sean\src\proj-secrets\a`, "", ErrAbsolutePathOutsideBase},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidatePathScoped(base, tt.requested, ScopeBaseOnly)
			if tt.wantErr != nil {
				if err != tt.wantErr {
					t.Fatalf("error = %v, want %v (got path %q)", err, tt.wantErr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ValidatePathScoped(%q, %q) = %q, want %q", base, tt.requested, got, tt.want)
			}
		})
	}
}

// A UNC workspace root behaves the same way.
func TestValidatePathScopedUNCWorkspace(t *testing.T) {
	const base = `\\server\share\proj`

	got, err := ValidatePathScoped(base, `src\main.go`, ScopeBaseOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := `\\server\share\proj\src\main.go`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	if _, err := ValidatePathScoped(base, `\\server\other\proj`, ScopeBaseOnly); err != ErrAbsolutePathOutsideBase {
		t.Fatalf("error = %v, want %v", err, ErrAbsolutePathOutsideBase)
	}
}

// ScopeAllowAbsolute still permits an absolute path outside the workspace.
func TestValidatePathScopedWindowsAllowAbsolute(t *testing.T) {
	const base = `C:\Users\sean\src\proj`
	outside := `C:\Users\sean\notes.md`

	got, err := ValidatePathScoped(base, outside, ScopeAllowAbsolute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != outside {
		t.Fatalf("got %q, want %q", got, outside)
	}
}
