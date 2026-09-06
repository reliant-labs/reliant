// Copyright (c) 2025 Reliant Labs
package ospath

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestIsAbs(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// POSIX
		{"posix absolute", "/Users/sean/src/proj", true},
		{"posix root", "/", true},
		{"posix with dotdot", "/a/../b", true},
		{"posix filename containing a backslash", `/home/a\b`, true},

		// Windows drive letter, backslash
		{"drive backslash", `C:\Users\sean\src\proj`, true},
		{"drive backslash root", `C:\`, true},
		{"lowercase drive", `d:\work`, true},

		// Windows drive letter, forward slash
		{"drive forwardslash", "C:/Users/sean/src/proj", true},
		{"drive forwardslash root", "C:/", true},

		// Bare drive
		{"bare drive root", "C:", true},

		// UNC
		{"unc backslash", `\\server\share\proj`, true},
		{"unc share only", `\\server\share`, true},
		{"unc forwardslash", "//server/share/proj", true},
		{"extended length prefix", `\\?\C:\proj`, true},

		// Rejected
		{"relative", "src/proj", false},
		{"relative dot", "./src", false},
		{"relative dotdot", "../src", false},
		{"windows relative", `src\proj`, false},
		{"drive relative", `C:proj`, false},
		{"drive relative no separator", "C:src/proj", false},
		{"driveless rooted", `\proj`, false},
		{"empty", "", false},
		{"whitespace only", "   ", false},
		{"tab and newline only", "\t\n", false},
		{"nul byte", "/etc/passwd\x00.txt", false},
		{"nul byte windows", "C:\\a\x00b", false},
		{"not a drive letter", "1:/proj", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAbs(tt.path); got != tt.want {
				t.Errorf("IsAbs(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestIsAbsIsNotHostSpecific is the regression this package exists for: the
// same input must be judged identically no matter which OS the server runs on.
func TestIsAbsIsNotHostSpecific(t *testing.T) {
	windowsPaths := []string{
		`C:\Users\sean\src\proj`,
		"C:/Users/sean/src/proj",
		`\\server\share\proj`,
	}
	for _, p := range windowsPaths {
		if !IsAbs(p) {
			t.Errorf("IsAbs(%q) = false; a Windows daemon's absolute path must be accepted on a %s server", p, runtime.GOOS)
		}
		if runtime.GOOS != "windows" && filepath.IsAbs(p) {
			t.Errorf("precondition changed: filepath.IsAbs(%q) is now true on %s", p, runtime.GOOS)
		}
	}
}

func TestClean(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"posix trailing slash", "/a/b/", "/a/b"},
		{"posix dotdot", "/a/b/../c", "/a/c"},
		{"posix double slash", "/a//b", "/a/b"},
		{"posix already clean", "/a/b", "/a/b"},

		{"drive backslash trailing", `C:\a\b\`, `C:\a\b`},
		{"drive backslash dotdot", `C:\a\b\..\c`, `C:\a\c`},
		{"drive backslash double", `C:\a\\b`, `C:\a\b`},
		{"drive backslash root", `C:\`, `C:\`},
		{"bare drive", "C:", "C:"},
		{"drive forwardslash preserved", "C:/a/b/", "C:/a/b"},
		{"drive forwardslash dotdot", "C:/a/b/../c", "C:/a/c"},

		{"unc", `\\server\share\a\b\`, `\\server\share\a\b`},
		{"unc dotdot", `\\server\share\a\..\b`, `\\server\share\b`},

		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Clean(tt.path); got != tt.want {
				t.Errorf("Clean(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestCleanNeverMixesSeparators pins the corollary of the host-OS bug: on a
// POSIX host, filepath.Clean leaves a Windows path's backslashes alone and any
// subsequent join inserts `/`. Clean must stay inside one convention.
func TestCleanNeverMixesSeparators(t *testing.T) {
	for _, p := range []string{`C:\src\proj\`, `C:\src\..\proj`, `\\srv\share\a\`} {
		got := Clean(p)
		if containsRune(got, '/') {
			t.Errorf("Clean(%q) = %q; a backslash-convention path must not gain forward slashes", p, got)
		}
	}
	for _, p := range []string{"C:/src/proj/", "C:/src/../proj"} {
		got := Clean(p)
		if containsRune(got, '\\') {
			t.Errorf("Clean(%q) = %q; a forwardslash-convention path must not gain backslashes", p, got)
		}
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

func TestBase(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"posix", "/Users/sean/src/proj", "proj"},
		{"posix trailing slash", "/Users/sean/src/proj/", "proj"},
		{"posix root", "/", "/"},

		{"drive backslash", `C:\Users\sean\src\proj`, "proj"},
		{"drive backslash trailing", `C:\Users\sean\src\proj\`, "proj"},
		{"drive forwardslash", "C:/Users/sean/src/proj", "proj"},
		{"drive root", `C:\`, `C:\`},
		{"bare drive", "C:", "C:"},

		{"unc", `\\server\share\proj`, "proj"},
		{"unc share only", `\\server\share`, `\\server\share`},

		{"relative windows", `src\proj`, "proj"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Base(tt.path); got != tt.want {
				t.Errorf("Base(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		elems []string
		want  string
	}{
		{"posix", "/a/b", []string{"c"}, "/a/b/c"},
		{"posix trailing slash base", "/a/b/", []string{"c"}, "/a/b/c"},
		{"posix multiple", "/a", []string{"b", "c"}, "/a/b/c"},

		{"drive backslash", `C:\a\b`, []string{"c"}, `C:\a\b\c`},
		{"drive backslash trailing base", `C:\a\b\`, []string{"c"}, `C:\a\b\c`},
		{"drive backslash nested elem", `C:\a`, []string{`b\c`}, `C:\a\b\c`},
		{"drive forwardslash", "C:/a/b", []string{"c"}, "C:/a/b/c"},
		{"unc", `\\srv\share`, []string{"proj"}, `\\srv\share\proj`},

		{"empty elem skipped", `C:\a`, []string{"", "b"}, `C:\a\b`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Join(tt.base, tt.elems...); got != tt.want {
				t.Errorf("Join(%q, %v) = %q, want %q", tt.base, tt.elems, got, tt.want)
			}
		})
	}
}
