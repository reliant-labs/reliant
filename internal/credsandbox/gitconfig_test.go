// Copyright (c) 2026 Reliant Labs

package credsandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// B1 (R13.B1): the values are what git 2.43's `git config -f <file> --list`
// prints for the same file: comments, quotes, escapes and continuations are
// decoded as git decodes them, so an include names the file git reads.
func TestParseGitConfig_DecodesValuesAsGitDoes(t *testing.T) {
	cfg := "\xef\xbb\xbf[Include]\n path = /srv/a.conf # shared ; x\n" +
		"[include]\n\tpath = \"/srv/b \\\"q\\\".conf\" ; c\n" +
		"[includeIf \"gitdir:~/w/\"]\r\n\tPATH = /srv/c\\\n.conf\n" +
		"[include] path = /srv/d\\tx   y  \n" +
		"[credential \"https://x\"]\n\thelper = \"!/opt/h get\" # c\n" +
		"[include]\n\tpath\n" +
		"[core.Sub]\n\tpath = /not\n"
	assert.Equal(t, []gitConfigEntry{
		{section: "include", key: "path", value: "/srv/a.conf"},
		{section: "include", key: "path", value: `/srv/b "q".conf`},
		{section: "includeif", key: "path", value: "/srv/c.conf"},
		{section: "include", key: "path", value: "/srv/d\tx   y"},
		{section: "credential", key: "helper", value: "!/opt/h get"},
		{section: "include", key: "path", value: ""},
		{section: "core", key: "path", value: "/not"},
	}, parseGitConfig([]byte(cfg)))
}

// A line git refuses ends the file for git, so it ends it here; what came
// before is still read.
func TestParseGitConfig_StopsWhereGitRefuses(t *testing.T) {
	cfg := "[include]\n\tpath = /srv/a.conf\n\tpath = \"/srv/unterminated\n\tpath = /srv/after.conf\n"
	assert.Equal(t, []gitConfigEntry{
		{section: "include", key: "path", value: "/srv/a.conf"},
	}, parseGitConfig([]byte(cfg)))
}
