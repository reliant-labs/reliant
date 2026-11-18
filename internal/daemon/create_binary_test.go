package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if _, err := WriteNewFile(link, nonUTF8); !errors.Is(err, ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("the symlink's target must not be created, stat err = %v", err)
	}
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
