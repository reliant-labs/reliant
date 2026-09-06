// Copyright (c) 2025 Reliant Labs

// Package ospath answers path questions about a path that belongs to some
// OTHER machine.
//
// `path/filepath` compiles to the rules of the OS the binary was built for.
// That is the correct authority for a path this process is about to open, and
// the wrong one for every path that arrives over the wire. Reliant is a
// distributed system in which only the daemon has a filesystem and the daemon
// may run on a different machine than the server (see reliant.md), so a Linux
// API server validating a Windows daemon's project path with
// filepath.IsAbs judges `C:\Users\sean\src\proj` to be relative and rejects it.
// Measured on darwin:
//
//	filepath.IsAbs(`C:\Users\sean\src\proj`) == false
//	filepath.IsAbs("C:/Users/sean/src/proj") == false
//	filepath.IsAbs(`\\server\share\proj`)    == false
//
// The functions here never consult runtime.GOOS. They accept both POSIX and
// Windows conventions and, critically, they normalize a path WITHIN its own
// convention: filepath.Clean on Linux leaves a Windows path's backslashes
// untouched and filepath.Join inserts `/`, which together produce
// mixed-separator garbage like `C:\Users\sean/src`.
//
// internal/daemonpolicy/paths.go reaches the same conclusion for glob patterns
// ("the daemon may be running on Windows") and this package generalizes it.
//
// Use filepath, NOT this package, for a path this process itself will open —
// its own config file, its own temp dir, its own log path. There the host OS
// genuinely is the authority.
package ospath

import (
	"path"
	"strings"
)

// convention is the separator style a path is written in.
type convention int

const (
	posix convention = iota
	windows
)

// styleOf classifies a path by convention.
//
// A leading `/` wins outright, because a POSIX filename may legally contain a
// backslash: `/home/a\b` is one POSIX file, not a Windows path, and treating
// it as Windows would rewrite its separators. Only after that does a drive
// letter, a UNC prefix, or the mere presence of a backslash imply Windows.
// A relative path containing a backslash (`a\b`) is therefore read as Windows;
// that ambiguity is unavoidable and only affects relative inputs.
func styleOf(p string) convention {
	if p == "" {
		return posix
	}
	if p[0] == '/' {
		return posix
	}
	if hasDrivePrefix(p) || strings.HasPrefix(p, `\\`) {
		return windows
	}
	if strings.ContainsRune(p, '\\') {
		return windows
	}
	return posix
}

// hasDrivePrefix reports whether p begins with a `X:` volume specifier.
func hasDrivePrefix(p string) bool {
	if len(p) < 2 || p[1] != ':' {
		return false
	}
	c := p[0]
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// IsAbs reports whether p is absolute under EITHER POSIX or Windows rules.
//
// Absolute:
//   - POSIX rooted:      /foo/bar, and //server/share (also valid POSIX)
//   - Windows drive:     C:\foo, C:/foo, and the bare drive root C:
//   - Windows UNC:       \\server\share, \\?\C:\foo
//
// Not absolute:
//   - relative:          foo/bar, ./foo, ..\foo
//   - drive-relative:    C:foo — names a location relative to that drive's
//     current directory, which no other machine can resolve
//   - Windows rooted-but-driveless: \foo — resolves against the caller's
//     current drive, which is likewise unknowable here
//   - empty or whitespace-only
//   - anything containing a NUL byte, which truncates the path at the syscall
//     boundary so what gets opened differs from what was checked
func IsAbs(p string) bool {
	if strings.TrimSpace(p) == "" {
		return false
	}
	if strings.ContainsRune(p, '\x00') {
		return false
	}
	if p[0] == '/' {
		return true
	}
	if p[0] == '\\' {
		// A single leading separator is drive-relative on Windows; only a UNC
		// or extended-length prefix (`\\`) names a root.
		return len(p) > 1 && (p[1] == '\\' || p[1] == '/')
	}
	if !hasDrivePrefix(p) {
		return false
	}
	// `C:` is the drive root. `C:\` and `C:/` are too. `C:foo` is not.
	return len(p) == 2 || p[2] == '\\' || p[2] == '/'
}

// volumeName returns p's leading volume specifier under Windows rules: the
// `X:` of a drive path, or the `\\server\share` of a UNC path. Everything
// after it is the path within that volume.
func volumeName(p string) string {
	if hasDrivePrefix(p) {
		return p[:2]
	}
	if len(p) < 2 || !isSep(p[0]) || !isSep(p[1]) {
		return ""
	}
	// \\server\share — consume exactly two more components.
	rest := p[2:]
	end := 0
	seen := 0
	for end < len(rest) {
		if isSep(rest[end]) {
			seen++
			if seen == 2 {
				break
			}
		}
		end++
	}
	return p[:2+end]
}

func isSep(c byte) bool { return c == '\\' || c == '/' }

// separatorOf picks the separator to emit for a Windows-convention path:
// whichever the caller already used, preferring the backslash when both
// appear. Preserving the caller's style keeps a round-tripped path
// byte-comparable, which matters because project paths are database keys.
func separatorOf(p string) byte {
	if strings.ContainsRune(p, '\\') {
		return '\\'
	}
	return '/'
}

// Clean lexically normalizes p within its own convention.
//
// It resolves `.` and `..`, collapses repeated separators, and drops a
// trailing separator — the same rules as path.Clean — but it never rewrites a
// Windows path into POSIX form or the reverse, and it preserves the volume
// specifier. Clean does no I/O and follows no symlinks.
func Clean(p string) string {
	if p == "" {
		return ""
	}
	if styleOf(p) == posix {
		return path.Clean(p)
	}

	vol := volumeName(p)
	rest := p[len(vol):]
	if rest == "" {
		return vol
	}

	sep := separatorOf(p)
	cleaned := path.Clean(strings.ReplaceAll(rest, `\`, "/"))
	if cleaned == "." && vol != "" {
		// `C:` + `.` is the drive root reference, not "C:\.".
		return vol
	}
	if sep == '\\' {
		cleaned = strings.ReplaceAll(cleaned, "/", `\`)
	}
	return vol + cleaned
}

// Base returns the last element of p, cleaned first.
//
// This is what filepath.Base cannot do for a foreign path: on Linux,
// filepath.Base(`C:\a\b`) returns the whole string, so a project created from
// a Windows daemon's discovery gets `C:\a\b` as its display name.
//
// A path with no element below its root — "/" or `C:\` — returns the cleaned
// path itself rather than a bare separator, because the only caller that wants
// a base name wants something displayable.
func Base(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	cleaned := Clean(p)
	if styleOf(cleaned) == posix {
		return path.Base(cleaned)
	}

	vol := volumeName(cleaned)
	rest := cleaned[len(vol):]
	trimmed := strings.TrimRight(rest, `\/`)
	if i := strings.LastIndexAny(trimmed, `\/`); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	if trimmed == "" {
		return cleaned
	}
	return trimmed
}

// Join appends elems to base using base's own separator, then cleans the
// result. Empty elements are skipped.
//
// filepath.Join is wrong here for the same reason Clean is: on Linux it joins
// with `/`, so Join(`C:\src`, "web") yields `C:\src/web`.
func Join(base string, elems ...string) string {
	if styleOf(base) == posix {
		parts := append([]string{base}, elems...)
		return path.Clean(strings.Join(nonEmpty(parts), "/"))
	}

	sep := string(separatorOf(base))
	parts := nonEmpty(append([]string{base}, elems...))
	if len(parts) == 0 {
		return ""
	}
	// Trim separators between segments so a caller-supplied leading or
	// trailing separator does not produce an empty component.
	joined := parts[0]
	for _, part := range parts[1:] {
		joined = strings.TrimRight(joined, `\/`) + sep + strings.TrimLeft(part, `\/`)
	}
	return Clean(joined)
}

func nonEmpty(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
