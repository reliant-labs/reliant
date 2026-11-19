package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestWriteNewFile_CreatesANewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shots", "new.png")
	if _, err := WriteNewFile(path, nonUTF8); err != nil {
		t.Fatalf("WriteNewFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, nonUTF8) {
		t.Fatalf("wrote % x, want % x", got, nonUTF8)
	}
}

func TestWriteNewFile_ExistingFileIsRefusedAndUntouched(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theirs.png")
	theirs := []byte("someone else's image")
	if err := os.WriteFile(path, theirs, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WriteNewFile(path, nonUTF8)
	if !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, theirs) {
		t.Fatalf("the existing file was changed to % x", got)
	}
}

func TestWriteNewFile_SymlinkIsRefusedNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			// Creating a symlink on Windows needs Developer Mode or
			// SeCreateSymbolicLinkPrivilege; without it there is nothing to test.
			t.Skipf("this Windows account may not create symlinks: %v", err)
		}
		t.Fatalf("os.Symlink: %v", err)
	}
	for name, stubLink := range map[string]bool{"hard link": false, "exclusive create fallback": true} {
		t.Run(name, func(t *testing.T) {
			if stubLink {
				stubLinkUnsupported(t)
			}
			if _, err := WriteNewFile(link, nonUTF8); !errors.Is(err, ErrFileExists) {
				t.Fatalf("err = %v, want ErrFileExists", err)
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("the symlink's target must not be created, stat err = %v", err)
			}
			assertOnlyEntries(t, dir, "link.png")
		})
	}
}

// stubLinkUnsupported makes WriteNewFile see a filesystem without hard links,
// so it takes the exclusive-create fallback. Tests that swap the seams are
// not parallel.
func stubLinkUnsupported(t *testing.T) {
	t.Helper()
	orig := linkFile
	linkFile = func(oldname, newname string) error {
		return &os.LinkError{Op: "link", Old: oldname, New: newname, Err: errors.ErrUnsupported}
	}
	t.Cleanup(func() { linkFile = orig })
}

// stubWrite replaces the content write for the duration of the test.
func stubWrite(t *testing.T, write func(f *os.File, content []byte) error) {
	t.Helper()
	orig := writeContent
	writeContent = write
	t.Cleanup(func() { writeContent = orig })
}

// assertOnlyEntries fails unless dir holds exactly the named entries: no
// partial file and no leftover temporary file.
func assertOnlyEntries(t *testing.T, dir string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s holds %q, want %q", dir, got, want)
	}
}

func TestWriteNewFile_FallbackWithoutHardLinksCreatesTheFile(t *testing.T) {
	stubLinkUnsupported(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "new.png")
	if _, err := WriteNewFile(path, nonUTF8); err != nil {
		t.Fatalf("WriteNewFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, nonUTF8) {
		t.Fatalf("wrote % x, want % x", got, nonUTF8)
	}
	assertOnlyEntries(t, dir, "new.png")
}

func TestWriteNewFile_FallbackRefusesAnExistingFile(t *testing.T) {
	stubLinkUnsupported(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "theirs.png")
	theirs := []byte("someone else's image")
	if err := os.WriteFile(path, theirs, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteNewFile(path, nonUTF8); !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, theirs) {
		t.Fatalf("the existing file was changed to % x", got)
	}
	assertOnlyEntries(t, dir, "theirs.png")
}

func TestWriteNewFile_FileCreatedDuringTheWriteIsNotReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	theirs := []byte("created by another process mid-write")
	stubWrite(t, func(f *os.File, content []byte) error {
		if err := os.WriteFile(path, theirs, 0o644); err != nil {
			return err
		}
		_, err := f.Write(content)
		return err
	})
	if _, err := WriteNewFile(path, nonUTF8); !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, theirs) {
		t.Fatalf("the other process's file was changed to % x", got)
	}
	assertOnlyEntries(t, dir, "shot.png")
}

func TestWriteNewFile_FailedWriteLeavesNothingBehind(t *testing.T) {
	for name, stubLink := range map[string]bool{"hard link": false, "exclusive create fallback": true} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "shot.png")
			if stubLink {
				stubLinkUnsupported(t)
			}
			stubWrite(t, func(f *os.File, content []byte) error {
				// With hard links the write that fails is the temporary
				// file's; in the fallback it is path's own.
				if stubLink && f.Name() != path {
					_, err := f.Write(content)
					return err
				}
				if _, err := f.Write(content[:1]); err != nil {
					return err
				}
				return errors.New("disk full")
			})
			if _, err := WriteNewFile(path, nonUTF8); err == nil {
				t.Fatal("a failed write must fail WriteNewFile")
			}
			assertOnlyEntries(t, dir)
		})
	}
}

// The fallback creates path itself, so a failed write must clean it up; but
// if another process removed it and put its own file there meanwhile, that
// file is theirs and must survive.
func TestWriteNewFile_FallbackCleanupNeverRemovesAnotherProcesssFile(t *testing.T) {
	stubLinkUnsupported(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	theirs := []byte("another process's file")
	stubWrite(t, func(f *os.File, content []byte) error {
		if f.Name() != path {
			_, err := f.Write(content) // the temporary file: write it normally
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := os.WriteFile(path, theirs, 0o644); err != nil {
			return err
		}
		return errors.New("disk full")
	})
	if _, err := WriteNewFile(path, nonUTF8); err == nil || errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want the write failure", err)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, theirs) {
		t.Fatalf("the other process's file must survive; read % x, err %v", got, err)
	}
	assertOnlyEntries(t, dir, "shot.png")
}

// errSender answers every command with a daemon-reported error.
type errSender struct {
	commandType string
	err         error
}

func (s *errSender) SendDaemonCommand(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
	s.commandType = commandType
	return nil, s.err
}

func TestRemoteCreateBinaryFile_ClassifiesTheDaemonsAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		daemonErr error
		want      error
	}{
		"exists": {
			daemonErr: fmt.Errorf("%w: /w/shot.png", ErrFileExists),
			want:      ErrFileExists,
		},
		"older daemon": {
			daemonErr: errors.New(`this machine is running an older version of Reliant (v1.4.0) that doesn't support "fs.create_binary_file" — update it to continue`),
			want:      ErrCreateUnsupported,
		},
		"oldest daemon": {
			daemonErr: errors.New(`unknown daemon command type: "fs.create_binary_file"`),
			want:      ErrCreateUnsupported,
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Errors cross the wire as text: only the message reaches the server.
			sender := &errSender{err: errors.New(tc.daemonErr.Error())}
			_, err := NewRemoteClient(sender, "user-1").CreateBinaryFile(context.Background(), "/w/shot.png", nonUTF8)
			if sender.commandType != CreateBinaryFileCommand {
				t.Fatalf("command = %q, want %q", sender.commandType, CreateBinaryFileCommand)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}

	sender := &errSender{err: errors.New("path is outside the directories this run may use")}
	_, err := NewRemoteClient(sender, "user-1").CreateBinaryFile(context.Background(), "/etc/x.png", nonUTF8)
	if err == nil || errors.Is(err, ErrFileExists) || errors.Is(err, ErrCreateUnsupported) {
		t.Fatalf("any other failure stays itself, got %v", err)
	}
}
