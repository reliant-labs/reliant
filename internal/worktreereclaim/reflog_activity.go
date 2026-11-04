// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	maxActivityReflogBytes = 4 << 20
	maxActivityReflogLine  = 64 << 10
)

// reflogActivity reads only a bounded, stable regular file. Missing logs (e.g.
// logging disabled or fully expired) add no signal: HEAD and directory mtimes
// still apply. Empty logs retain their mtime, protecting a newly created or
// truncated log without letting rewrites of nonempty history reset the clock.
func reflogActivity(ctx context.Context, path string) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	before, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if !before.Mode().IsRegular() || before.Size() > maxActivityReflogBytes {
		return time.Time{}, fmt.Errorf("cannot inspect reflog %s: not a regular file or too large", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	return stableReflogActivity(ctx, path, f, before, f)
}

func stableReflogActivity(ctx context.Context, path string, f *os.File, before os.FileInfo, src io.Reader) (time.Time, error) {
	opened, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}
	if !sameReflog(before, opened) {
		return time.Time{}, fmt.Errorf("reflog changed while opening %s", path)
	}
	newest, n, err := readReflogActivity(ctx, io.LimitReader(src, maxActivityReflogBytes+1))
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot inspect reflog %s: %w", path, err)
	}
	after, err := f.Stat()
	if err != nil {
		return time.Time{}, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return time.Time{}, err
	}
	if !sameReflog(before, after) || !sameReflog(before, current) || n != before.Size() {
		return time.Time{}, fmt.Errorf("reflog changed while reading %s", path)
	}
	if n == 0 {
		newest = before.ModTime()
	}
	return newest, ctx.Err()
}

func sameReflog(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func readReflogActivity(ctx context.Context, src io.Reader) (time.Time, int64, error) {
	r := bufio.NewReaderSize(src, maxActivityReflogLine)
	var newest time.Time
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return time.Time{}, n, err
		}
		line, err := r.ReadSlice('\n')
		n += int64(len(line))
		if n > maxActivityReflogBytes {
			return time.Time{}, n, fmt.Errorf("reflog exceeds byte limit")
		}
		if err == io.EOF && len(line) == 0 {
			return newest, n, ctx.Err()
		}
		if err != nil { // Includes a long line or a torn final record without '\n'.
			return time.Time{}, n, fmt.Errorf("incomplete reflog record: %w", err)
		}
		event, err := reflogEventTime(string(line[:len(line)-1]))
		if err != nil {
			return time.Time{}, n, err
		}
		if event.After(newest) {
			newest = event
		}
	}
}

// A raw reflog record is "old new ident unix-seconds timezone\tmessage".
// Unix seconds already identify the instant; the display offset is validated,
// not applied. Inspect every record: clocks can go backwards between events.
func reflogEventTime(line string) (time.Time, error) {
	bad := func() (time.Time, error) { return time.Time{}, fmt.Errorf("malformed reflog record") }
	// Git's initial linked-worktree record has no message or tab separator.
	header, _, _ := strings.Cut(line, "\t")
	parts := strings.SplitN(header, " ", 3)
	if len(parts) != 3 || len(parts[0]) != len(parts[1]) || (len(parts[0]) != 40 && len(parts[0]) != 64) {
		return bad()
	}
	for _, oid := range parts[:2] {
		if _, err := hex.DecodeString(oid); err != nil {
			return bad()
		}
	}
	end := strings.LastIndex(parts[2], "> ")
	if end < 0 || !strings.Contains(parts[2][:end], "<") {
		return bad()
	}
	fields := strings.Split(parts[2][end+2:], " ")
	if len(fields) != 2 || fields[0] == "" {
		return bad()
	}
	for _, c := range fields[0] {
		if c < '0' || c > '9' {
			return bad()
		}
	}
	seconds, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || seconds > 253402300799 { // Beyond year 9999: uncertain, never ancient.
		return bad()
	}
	offset := fields[1]
	if len(offset) != 5 || (offset[0] != '+' && offset[0] != '-') {
		return bad()
	}
	for _, c := range offset[1:] {
		if c < '0' || c > '9' {
			return bad()
		}
	}
	hours, _ := strconv.Atoi(offset[1:3])
	minutes, _ := strconv.Atoi(offset[3:])
	if hours > 23 || minutes > 59 {
		return bad()
	}
	return time.Unix(seconds, 0), nil
}
