// Copyright (c) 2025 Reliant Labs
package tools_daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNormalizeProjectPath_WindowsPaths covers the helper that gates every
// project-config snapshot and delta from a daemon. Returning "" here is a
// silent, unrecoverable drop (see the comment in persistProjectConfigSnapshot).
func TestNormalizeProjectPath_WindowsPaths(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"posix", "/home/workspace/projects/app", "/home/workspace/projects/app"},
		{"posix trailing slash", "/home/workspace/projects/app/", "/home/workspace/projects/app"},
		{"drive backslash", `C:\Users\sean\src\proj`, `C:\Users\sean\src\proj`},
		{"drive backslash trailing", `C:\Users\sean\src\proj\`, `C:\Users\sean\src\proj`},
		{"drive backslash dotdot", `C:\Users\sean\x\..\proj`, `C:\Users\sean\proj`},
		{"drive forward slash", "C:/Users/sean/src/proj", "C:/Users/sean/src/proj"},
		{"unc", `\\server\share\proj`, `\\server\share\proj`},

		{"relative rejected", "src/proj", ""},
		{"drive relative rejected", `C:proj`, ""},
		{"empty rejected", "", ""},
		{"whitespace rejected", "   ", ""},
		{"nul rejected", "/a\x00b", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeProjectPath(tt.in))
		})
	}
}
