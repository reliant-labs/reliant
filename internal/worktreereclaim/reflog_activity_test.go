// Copyright (c) 2025 Reliant Labs
package worktreereclaim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func activityRecord(at time.Time, offset string) string {
	return fmt.Sprintf("%s %s Test User <test@example.com> %d %s\tcheckout: moving to branch\n",
		strings.Repeat("0", 40), strings.Repeat("a", 40), at.Unix(), offset)
}

func setIdleMtime(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// No git processes: only the linked-worktree files the idle scan actually reads.
func idleFixture(t *testing.T, n int) (string, []string, []string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	var checkouts, admins []string
	old := time.Unix(1700000000, 0)
	for i := range n {
		c := filepath.Join(root, fmt.Sprint(i))
		a := filepath.Join(base, "admin", fmt.Sprint(i))
		write(t, filepath.Join(c, ".git"), "gitdir: "+a+"\n")
		write(t, filepath.Join(a, "HEAD"), "ref: refs/heads/test\n")
		write(t, filepath.Join(a, "index"), "index")
		write(t, filepath.Join(a, "logs", "HEAD"), activityRecord(old.Add(-time.Hour), "+0000"))
		for _, p := range []string{c, filepath.Join(a, "HEAD"), filepath.Join(a, "index"), filepath.Join(a, "logs", "HEAD")} {
			setIdleMtime(t, p, old)
		}
		checkouts, admins = append(checkouts, c), append(admins, a)
	}
	setIdleMtime(t, root, old)
	return root, checkouts, admins
}

func TestIdleSince_FallbacksAndAllCheckoutSignals(t *testing.T) {
	t.Parallel()
	old := time.Unix(1700000000, 0)
	recent := old.Add(72 * time.Hour)
	for _, signal := range []string{"workspace", "checkout", "HEAD", "missing", "empty", "old empty", "future event", "other checkout event", "malformed", "truncated", "oversized", "not regular"} {
		t.Run(signal, func(t *testing.T) {
			path, cs, admins := idleFixture(t, 2)
			log := filepath.Join(admins[1], "logs", "HEAD")
			want := recent
			wantErr := false
			switch signal {
			case "workspace":
				setIdleMtime(t, path, recent)
			case "checkout":
				setIdleMtime(t, cs[1], recent)
			case "HEAD":
				setIdleMtime(t, filepath.Join(admins[1], "HEAD"), recent)
			case "missing":
				if err := os.Remove(log); err != nil {
					t.Fatal(err)
				}
				want = old
			case "empty", "old empty":
				write(t, log, "")
				if signal == "old empty" {
					want = old
				}
				setIdleMtime(t, log, want)
			case "future event", "other checkout event":
				if signal == "future event" {
					want = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				write(t, log, activityRecord(want, "+1245"))
				setIdleMtime(t, log, old)
			case "malformed":
				write(t, log, activityRecord(old, "+0000")+"bad record\n")
				wantErr = true
			case "truncated":
				write(t, log, strings.TrimSuffix(activityRecord(old, "+0000"), "\n"))
				wantErr = true
			case "oversized":
				f, err := os.OpenFile(log, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(maxActivityReflogBytes + 1)
				_ = f.Close()
				if err != nil {
					t.Fatal(err)
				}
				wantErr = true
			case "not regular":
				if err := os.Remove(log); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(log, 0o755); err != nil {
					t.Fatal(err)
				}
				wantErr = true
			}
			got, err := idleSince(context.Background(), path, cs)
			if wantErr {
				if err == nil {
					t.Fatalf("uncertain activity returned %v without error", got)
				}
			} else if err != nil || !got.Equal(want) {
				t.Fatalf("idle = %v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestReadReflogActivity_RejectsUncertainRecords(t *testing.T) {
	t.Parallel()
	valid := activityRecord(time.Unix(1700000000, 0), "+0000")
	for name, data := range map[string]string{
		"no timestamp": strings.Replace(valid, "1700000000 ", "", 1),
		"offset":       strings.Replace(valid, "+0000", "+2460", 1),
		"overflow":     strings.Replace(valid, "1700000000", "9223372036854775808", 1),
		"negative":     strings.Replace(valid, "1700000000", "-1", 1),
		"no ident":     strings.Replace(valid, "Test User <test@example.com>", "", 1),
		"bad oid":      "z" + valid[1:],
		"no tab":       strings.ReplaceAll(valid, "\t", " "),
		"blank line":   valid + "\n",
		"long line":    valid + strings.Repeat("x", maxActivityReflogLine) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := readReflogActivity(context.Background(), strings.NewReader(data)); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
	// Initial linked-worktree records can omit both tab and message.
	initial, _, _ := strings.Cut(valid, "\t")
	if _, _, err := readReflogActivity(context.Background(), strings.NewReader(initial+"\n")); err != nil {
		t.Fatal(err)
	}
	// SHA-256 repositories use 64-character object IDs.
	sha256 := strings.ReplaceAll(strings.ReplaceAll(valid, strings.Repeat("0", 40), strings.Repeat("0", 64)), strings.Repeat("a", 40), strings.Repeat("a", 64))
	if _, _, err := readReflogActivity(context.Background(), strings.NewReader(sha256)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readReflogActivity(context.Background(), &activityErrorReader{}); err == nil {
		t.Fatal("read error ignored")
	}
	// Repeated short records hit the total limit, not the per-line limit.
	if _, _, err := readReflogActivity(context.Background(), io.LimitReader(&activityRepeatReader{line: valid}, maxActivityReflogBytes+1)); err == nil {
		t.Fatal("byte limit ignored")
	}
}

type activityErrorReader struct{}

func (*activityErrorReader) Read([]byte) (int, error) { return 0, errors.New("unreadable") }

type activityRepeatReader struct{ line string }

func (r *activityRepeatReader) Read(b []byte) (int, error) { return copy(b, r.line), nil }

// Reader hooks make cancellation and mutation deterministic, without sleeps or
// races. The file is changed between its pre/post-read metadata snapshots.
type activityHookReader struct {
	src  io.Reader
	hook func()
}

func (r *activityHookReader) Read(b []byte) (int, error) { r.hook(); return r.src.Read(b) }

func TestReflogActivity_CancellationAndChanges(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	reader := &activityHookReader{strings.NewReader(activityRecord(time.Unix(1700000000, 0), "+0000")), cancel}
	if _, _, err := readReflogActivity(ctx, reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	path, cs, _ := idleFixture(t, 1)
	if _, err := idleSince(ctx, path, cs); !errors.Is(err, context.Canceled) {
		t.Fatalf("idle cancel: %v", err)
	}
	if _, err := reflogActivity(ctx, "unused"); !errors.Is(err, context.Canceled) {
		t.Fatalf("file cancel: %v", err)
	}

	file := filepath.Join(t.TempDir(), "HEAD")
	write(t, file, "old")
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	setIdleMtime(t, file, before.ModTime().Add(time.Hour))
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if sameReflog(before, after) {
		t.Fatal("mtime change accepted")
	}
	write(t, file, "longer")
	after, err = os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if sameReflog(before, after) {
		t.Fatal("size change accepted")
	}
	other := filepath.Join(t.TempDir(), "HEAD")
	write(t, other, "old")
	setIdleMtime(t, other, before.ModTime())
	after, err = os.Stat(other)
	if err != nil {
		t.Fatal(err)
	}
	if sameReflog(before, after) {
		t.Fatal("replacement with identical size/mtime accepted")
	}
}
