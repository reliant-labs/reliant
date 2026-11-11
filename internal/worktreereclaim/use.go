// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Path use. The server's quiet check sees the chats and threads bound to a
// worktree row, and nothing else. On 2026-11-10 an agent whose chat was bound
// to another workspace worked in one by absolute path; the row looked quiet
// for hours and was archived under it. Only the daemon sees that use, in the
// tool calls and commands it runs, so it remembers, per workspace, the last
// time one of them named a path inside it, and reports it with every answer
// about an active worktree (Result.LastUse).
//
// Naming is enough. A tool call whose input mentions a workspace's path (a
// file to read, a directory to run in, a `cd` in a shell command) is use of
// it, read-only use included: reading leaves no trace on disk for any later
// check to find. Over-counting only keeps a worktree longer.

// useDepth is how many path components below a root a mention is recorded at.
// A workspace is <root>/<project>/<name> (older ones <root>/<id>); a deeper
// mention is recorded at every depth up to this, so whichever layout a row
// has, its path is one of the keys.
const useDepth = 3

// useFlushEvery bounds how often the log is written to disk: a tool batch
// is recorded in memory at once, and persisted at most this often, so a
// daemon restart loses at most this much of it.
const useFlushEvery = time.Minute

// UseFile is the log's name in the reclaim state directory.
const UseFile = "use.json"

// UseLog remembers when each workspace under a worktree root was last named
// by work the daemon ran. It persists hashes of the workspace paths and times,
// never paths. Safe for concurrent use.
type UseLog struct {
	mu        sync.Mutex
	root      string   // the canonical root every key is under
	aliases   []string // other spellings of root (a symlink's target, ~/...)
	statePath string
	now       func() time.Time
	last      map[string]time.Time // hashed workspace path -> last use
	dirty     bool
	flushedAt time.Time
}

// NewUseLog returns the use log for root, persisted at statePath ("" keeps it
// in memory only) and restored from it. aliases are other spellings of root
// a tool input may use: its symlink-resolved form, "~/.reliant/worktrees".
func NewUseLog(root, statePath string, now func() time.Time, aliases ...string) *UseLog {
	if now == nil {
		now = time.Now
	}
	u := &UseLog{root: filepath.Clean(root), statePath: statePath, now: now, last: map[string]time.Time{}}
	for _, a := range aliases {
		if a = strings.TrimSuffix(a, "/"); a != "" && a != u.root {
			u.aliases = append(u.aliases, a)
		}
	}
	if statePath != "" {
		if b, err := os.ReadFile(statePath); err == nil {
			var saved map[string]int64
			if json.Unmarshal(b, &saved) == nil {
				for k, ns := range saved {
					u.last[k] = time.Unix(0, ns)
				}
			}
		}
	}
	return u
}

// Note records use of every workspace under the root that text names. text
// is anything a tool call or command carries: its JSON input, a working
// directory, a shell command.
func (u *UseLog) Note(text string) {
	if u == nil || text == "" {
		return
	}
	var keys []string
	for _, prefix := range append([]string{u.root}, u.aliases...) {
		keys = appendMentions(keys, text, prefix, u.root)
	}
	if len(keys) == 0 {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	for _, k := range keys {
		if now.After(u.last[k]) {
			u.last[k] = now
			u.dirty = true
		}
	}
	if u.dirty && u.statePath != "" && now.Sub(u.flushedAt) >= useFlushEvery {
		u.flushLocked(now)
	}
}

// LastUse is the last recorded use of the workspace at path, or zero.
func (u *UseLog) LastUse(path string) time.Time {
	if u == nil {
		return time.Time{}
	}
	key, ok := u.keyFor(filepath.Clean(path))
	if !ok {
		return time.Time{}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last[key]
}

// Flush writes what is recorded to disk now.
func (u *UseLog) Flush() {
	if u == nil || u.statePath == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.dirty {
		u.flushLocked(u.now())
	}
}

func (u *UseLog) flushLocked(now time.Time) {
	saved := make(map[string]int64, len(u.last))
	for k, t := range u.last {
		saved[k] = t.UnixNano()
	}
	b, err := json.Marshal(saved)
	if err != nil {
		return
	}
	// A failed write is retried at the next flush; the memory copy still
	// answers until the daemon restarts.
	if writeFileAtomic(u.statePath, b) == nil {
		u.dirty, u.flushedAt = false, now
	}
}

// keyFor is the hashed key of the workspace at path, which must be under the
// root (in any of its spellings) and at most useDepth below it.
func (u *UseLog) keyFor(path string) (string, bool) {
	for _, prefix := range append([]string{u.root}, u.aliases...) {
		rel, ok := strings.CutPrefix(path, prefix+"/")
		if !ok || rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		if len(parts) > useDepth {
			parts = parts[:useDepth]
		}
		return useKey(u.root + "/" + strings.Join(parts, "/")), true
	}
	return "", false
}

func useKey(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

// appendMentions appends the keys of every workspace text names under
// prefix, recorded as under root, at each depth from 1 to useDepth.
func appendMentions(keys []string, text, prefix, root string) []string {
	needle := prefix + "/"
	for i := 0; ; {
		j := strings.Index(text[i:], needle)
		if j < 0 {
			return keys
		}
		// Where the mention starts is not checked: a longer path that merely
		// contains the root (/x/home/me/.reliant/worktrees/...) over-counts,
		// which only keeps a worktree longer.
		i += j + len(needle)
		var parts []string
		for len(parts) < useDepth {
			k := i
			for k < len(text) && pathChar(text[k]) {
				k++
			}
			c := text[i:k]
			if c == "" || c == "." || c == ".." {
				break // never resolve upwards out of what was named
			}
			parts = append(parts, c)
			i = k
			if i >= len(text) || text[i] != '/' {
				break
			}
			i++
		}
		for d := 1; d <= len(parts); d++ {
			keys = append(keys, useKey(root+"/"+strings.Join(parts[:d], "/")))
		}
	}
}

// pathChar reports whether c can be part of a path component as a tool
// input spells it. It stops at what ends a path in JSON or a shell command:
// whitespace, quotes, a backslash escape, shell operators, and a slash.
func pathChar(c byte) bool {
	switch c {
	case '/', '"', '\'', '`', '\\', ';', '&', '|', '<', '>', '(', ')', '{', '}', '[', ']', ',', '$', '*', '?':
		return false
	}
	return c > ' ' && c != 0x7f
}
