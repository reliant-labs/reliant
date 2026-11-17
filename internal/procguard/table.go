package procguard

import (
	"bytes"
	"strconv"
	"time"
)

// Platform-neutral parsers and decisions behind the per-OS lookups, kept here
// so every platform's rules are tested on every platform.

// parseProcStat extracts the parent and process group from the contents of a
// Linux /proc/<pid>/stat:
//
//	1234 (some (odd) name) S 1200 1234 ...
//	pid  comm               st ppid pgrp
//
// comm is the executable name in parentheses and may itself contain spaces
// and parentheses, so the fields are read after the LAST ')' — the only
// unambiguous delimiter the format has.
func parseProcStat(data []byte) (Entry, bool) {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return Entry{}, false
	}
	fields := bytes.Fields(data[end+1:])
	// fields[0] = state, fields[1] = ppid, fields[2] = pgrp
	if len(fields) < 3 {
		return Entry{}, false
	}
	ppid, err := strconv.Atoi(string(fields[1]))
	if err != nil {
		return Entry{}, false
	}
	pgid, err := strconv.Atoi(string(fields[2]))
	if err != nil {
		return Entry{}, false
	}
	return Entry{PPID: ppid, PGID: pgid}, true
}

// trustedParent decides whether a Windows parent link names the real parent.
//
// Windows records the creator's pid at creation and never re-parents, while
// recycling pids quickly. A recorded parent is therefore only the real one
// when it is alive now and was created no later than the child; anything else
// — an unreadable creation time included — is a stranger wearing a dead
// parent's pid, and the walk stops rather than protect it.
func trustedParent(child, parent time.Time, childOK, parentOK bool) bool {
	return childOK && parentOK && !parent.After(child)
}
