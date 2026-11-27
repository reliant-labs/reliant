package devtelemetry

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPathsLiveUnderTheProjectsObsDirectory(t *testing.T) {
	dir := filepath.Join("proj", "x")
	obs := filepath.Join(dir, ".forge", "obs")
	for name, tc := range map[string]struct{ got, want string }{
		"headers":         {HeadersPath(dir), filepath.Join(obs, "otlp-headers")},
		"browser headers": {BrowserHeadersPath(dir), filepath.Join(obs, "otlp-headers-browser")},
		"notice":          {NoticePath(dir), filepath.Join(obs, "hosted-notice-shown")},
		"state":           {StatePath(dir, "prod"), filepath.Join(obs, "telemetry-prod.json")},
		"record":          {Path(dir, "prod"), filepath.Join(obs, "backend-prod.json")},
	} {
		if tc.got != tc.want {
			t.Errorf("%s path = %q, want %q", name, tc.got, tc.want)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Both tokens land in their own file in the form the OTLP exporter reads, and
// the directory carries a .gitignore so a token is never committed.
func TestWriteHeadersWritesBothTokensAndAGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHeaders(dir, "proc-token", "browser-token"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, HeadersPath(dir)); got != "authorization=Bearer proc-token\n" {
		t.Errorf("process headers = %q", got)
	}
	if got := readFile(t, BrowserHeadersPath(dir)); got != "authorization=Bearer browser-token\n" {
		t.Errorf("browser headers = %q", got)
	}
	if got := readFile(t, filepath.Join(filepath.Dir(HeadersPath(dir)), ".gitignore")); got != "*\n" {
		t.Errorf(".gitignore = %q, want it to ignore everything beside it", got)
	}
}

// A renewal replaces the token in place and leaves no temp file behind.
func TestWriteHeadersReplacesTokensAtomically(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHeaders(dir, "old", "old-b"); err != nil {
		t.Fatal(err)
	}
	if err := WriteHeaders(dir, "new", "new-b"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, HeadersPath(dir)); got != "authorization=Bearer new\n" {
		t.Errorf("headers = %q after a renewal", got)
	}
	entries, err := os.ReadDir(filepath.Dir(HeadersPath(dir)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := ".gitignore otlp-headers otlp-headers-browser"; strings.Join(names, " ") != want {
		t.Errorf("obs dir = %v, want only %q (no leftover temp files)", names, want)
	}
}

func TestWriteHeadersKeepsAnExistingGitignore(t *testing.T) {
	dir := t.TempDir()
	obs := filepath.Dir(HeadersPath(dir))
	if err := os.MkdirAll(obs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(obs, ".gitignore"), []byte("custom\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteHeaders(dir, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(obs, ".gitignore")); got != "custom\n" {
		t.Errorf(".gitignore was overwritten: %q", got)
	}
}

func TestWriteHeadersFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no unix permission bits")
	}
	dir := t.TempDir()
	if err := WriteHeaders(dir, "a", "b"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{HeadersPath(dir), BrowserHeadersPath(dir)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", filepath.Base(p), info.Mode().Perm())
		}
	}
}

// When .forge is a plain file nothing can be created under it, and every
// writer says so instead of pretending the token was stored.
func TestWritersFailWhenTheObsDirectoryCannotBeCreated(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".forge"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteHeaders(dir, "a", "b"); err == nil {
		t.Error("WriteHeaders succeeded without a directory to write into")
	}
	if err := WriteState(dir, State{Env: "dev"}); err == nil {
		t.Error("WriteState succeeded without a directory to write into")
	}
	if err := MarkNoticeShown(dir, time.Now()); err == nil {
		t.Error("MarkNoticeShown succeeded without a directory to write into")
	}
	if err := Write(dir, Record{Env: "dev"}); err == nil {
		t.Error("Write succeeded without a directory to write into")
	}
	if NoticeShown(dir) {
		t.Error("NoticeShown is true although nothing was recorded")
	}
}

func TestStateRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := ReadState(dir, "prod"); ok || err != nil {
		t.Fatalf("ReadState with nothing written = ok %v, err %v", ok, err)
	}
	want := State{
		Env:        "prod",
		ExpiresAt:  time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC),
		RenewedAt:  time.Date(2026, 11, 30, 10, 0, 0, 0, time.UTC),
		Retention:  Retention{TracesDays: 7, MetricsDays: 30, LogsDays: 14, BrowserDays: 3},
		RenewError: "token endpoint returned 503",
	}
	if err := WriteState(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadState(dir, "prod")
	if err != nil || !ok {
		t.Fatalf("ReadState = ok %v, err %v", ok, err)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) || !got.RenewedAt.Equal(want.RenewedAt) || got.Env != want.Env || got.Retention != want.Retention || got.RenewError != want.RenewError {
		t.Errorf("state = %+v, want %+v", got, want)
	}
	// States are per env.
	if _, ok, _ := ReadState(dir, "staging"); ok {
		t.Error("staging read prod's state")
	}
	if !strings.HasSuffix(readFile(t, StatePath(dir, "prod")), "}\n") {
		t.Error("state file should end with a newline")
	}
	// An empty RenewError is omitted from the file.
	want.RenewError = ""
	if err := WriteState(dir, want); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, StatePath(dir, "prod")), "renew_error") {
		t.Error("an empty renew_error should not be written")
	}
}

func TestReadStateErrors(t *testing.T) {
	dir := t.TempDir()
	if err := WriteState(dir, State{Env: "dev"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StatePath(dir, "dev"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadState(dir, "dev"); err == nil || ok {
		t.Errorf("corrupt state: ok %v, err %v; want an error", ok, err)
	}
	// A path that is a directory is a read error, not "no state".
	if err := os.Remove(StatePath(dir, "dev")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(StatePath(dir, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ReadState(dir, "dev"); err == nil || ok || errors.Is(err, os.ErrNotExist) {
		t.Errorf("unreadable state: ok %v, err %v; want a non-NotExist error", ok, err)
	}
}

func TestNoticeShownAfterMarking(t *testing.T) {
	dir := t.TempDir()
	if NoticeShown(dir) {
		t.Fatal("notice reported shown in a fresh project")
	}
	at := time.Date(2026, 11, 26, 9, 30, 0, 0, time.FixedZone("x", 3600))
	if err := MarkNoticeShown(dir, at); err != nil {
		t.Fatal(err)
	}
	if !NoticeShown(dir) {
		t.Error("notice not reported shown after MarkNoticeShown")
	}
	if got := readFile(t, NoticePath(dir)); got != "2026-11-26T08:30:00Z\n" {
		t.Errorf("notice file = %q, want the UTC time", got)
	}
	// Other projects are unaffected.
	if NoticeShown(t.TempDir()) {
		t.Error("another project shows the notice as shown")
	}
}

func writeRecord(t *testing.T, dir, env, backend string) {
	t.Helper()
	if err := Write(dir, Record{Env: env, Backend: backend, Reason: "test"}); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Deciding one env local removes its own grant. The headers files are per
// project, so they stay while another env still ships to the hosted store.
func TestClearCredentials(t *testing.T) {
	setup := func(t *testing.T) string {
		dir := t.TempDir()
		if err := WriteHeaders(dir, "p", "b"); err != nil {
			t.Fatal(err)
		}
		if err := WriteState(dir, State{Env: "dev"}); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("no other env: state and headers go", func(t *testing.T) {
		dir := setup(t)
		if err := ClearCredentials(dir, "dev"); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{StatePath(dir, "dev"), HeadersPath(dir), BrowserHeadersPath(dir)} {
			if exists(p) {
				t.Errorf("%s survived", filepath.Base(p))
			}
		}
	})
	t.Run("another env is hosted: only this env's state goes", func(t *testing.T) {
		dir := setup(t)
		writeRecord(t, dir, "prod", "cloud")
		if err := ClearCredentials(dir, "dev"); err != nil {
			t.Fatal(err)
		}
		if exists(StatePath(dir, "dev")) {
			t.Error("dev's state survived")
		}
		if !exists(HeadersPath(dir)) || !exists(BrowserHeadersPath(dir)) {
			t.Error("the shared headers were removed while prod still ships hosted")
		}
	})
	t.Run("another env is local: headers go", func(t *testing.T) {
		dir := setup(t)
		writeRecord(t, dir, "prod", "local")
		if err := ClearCredentials(dir, "dev"); err != nil {
			t.Fatal(err)
		}
		if exists(HeadersPath(dir)) {
			t.Error("headers survived although no other env is hosted")
		}
	})
	t.Run("this env's own hosted record does not keep the headers", func(t *testing.T) {
		dir := setup(t)
		writeRecord(t, dir, "dev", "cloud")
		if err := ClearCredentials(dir, "dev"); err != nil {
			t.Fatal(err)
		}
		if exists(HeadersPath(dir)) {
			t.Error("headers survived: an env being cleared must not count as another hosted env")
		}
	})
	t.Run("an unreadable other record does not keep the headers", func(t *testing.T) {
		dir := setup(t)
		if err := os.WriteFile(Path(dir, "prod"), []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ClearCredentials(dir, "dev"); err != nil {
			t.Fatal(err)
		}
		if exists(HeadersPath(dir)) {
			t.Error("headers survived because of a corrupt record")
		}
	})
	t.Run("nothing on disk is not an error", func(t *testing.T) {
		if err := ClearCredentials(t.TempDir(), "dev"); err != nil {
			t.Errorf("ClearCredentials on an empty project = %v", err)
		}
	})
	t.Run("a file that cannot be removed is reported", func(t *testing.T) {
		dir := setup(t)
		// A non-empty directory where the state file should be cannot be os.Remove'd.
		if err := os.Remove(StatePath(dir, "dev")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(StatePath(dir, "dev"), "child"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := ClearCredentials(dir, "dev"); err == nil {
			t.Error("ClearCredentials hid a removal failure")
		}
		if exists(HeadersPath(dir)) {
			t.Error("the headers should still be removed when the state removal fails")
		}
	})
}
