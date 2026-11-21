package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func staticSource(rel Release) Source {
	return SourceFunc(func(context.Context, string) (Release, error) { return rel, nil })
}

func TestNewRefusesUnsignedByDefault(t *testing.T) {
	t.Parallel()
	base := Config{Binary: "app", Current: Build{Version: "1.0.0"}, Source: staticSource(Release{}), StateDir: t.TempDir()}

	if _, err := New(base); !errors.Is(err, ErrNoVerifier) {
		t.Fatalf("New with no Verifier: err = %v, want ErrNoVerifier", err)
	}

	both := base
	both.Verifier, both.InsecureAllowUnsigned = &recordingVerifier{}, true
	if _, err := New(both); err == nil {
		t.Fatal("New with a Verifier AND InsecureAllowUnsigned must refuse the contradiction")
	}

	var logs bytes.Buffer
	insecure := base
	insecure.InsecureAllowUnsigned = true
	insecure.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	if _, err := New(insecure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "INSECURE") {
		t.Fatalf("the insecure opt-out must be logged at start; logs: %s", logs.String())
	}
}

func TestNewValidatesAndDefaults(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]Config{
		"no binary":  {Current: Build{Version: "1"}, Source: staticSource(Release{}), Verifier: &recordingVerifier{}},
		"no source":  {Binary: "app", Current: Build{Version: "1"}, Verifier: &recordingVerifier{}},
		"no current": {Binary: "app", Source: staticSource(Release{}), Verifier: &recordingVerifier{}},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New must refuse", name)
		}
	}
	u, err := New(Config{Binary: "app", Current: Build{Version: "1"}, Source: staticSource(Release{}), Verifier: &recordingVerifier{}, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := u.cfg.Policy.(StageOnly); !ok {
		t.Errorf("default Policy = %T, want StageOnly", u.cfg.Policy)
	}
	if u.cfg.ConfirmWithin != DefaultConfirmWithin || u.cfg.Probe[0] != "version" || u.cfg.ChecksumsName != "checksums.txt" {
		t.Errorf("defaults not filled: %+v", u.cfg)
	}
	if got := u.cfg.ArchiveName("linux", "arm64"); got != "app_linux_arm64.tar.gz" {
		t.Errorf("default ArchiveName = %s", got)
	}
}

type countingPolicy struct {
	calls int
	d     Decision
}

func (p *countingPolicy) Decide(context.Context, Build, Release) Decision {
	p.calls++
	return p.d
}

func TestCheck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mk := func(t *testing.T, cur string, rel Release, pol Policy) *Updater {
		u, err := New(Config{Binary: "app", Current: Build{Version: cur}, Source: staticSource(rel), Verifier: &recordingVerifier{}, Policy: pol, StateDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	t.Run("not newer skips without asking the policy", func(t *testing.T) {
		pol := &countingPolicy{d: Decision{Action: Apply}}
		_, d, err := mk(t, "1.2.0", Release{Version: "1.2.0"}, pol).Check(ctx)
		if err != nil || d.Action != Skip || pol.calls != 0 {
			t.Fatalf("d=%+v err=%v calls=%d", d, err, pol.calls)
		}
	})
	t.Run("unorderable skips", func(t *testing.T) {
		_, d, err := mk(t, "dev", Release{Version: "1.2.0"}, nil).Check(ctx)
		if err != nil || d.Action != Skip || !strings.Contains(d.Reason, "cannot order") {
			t.Fatalf("d=%+v err=%v", d, err)
		}
	})
	t.Run("newer asks the policy", func(t *testing.T) {
		pol := &countingPolicy{d: Decision{Action: Apply, Urgency: Security}}
		rel, d, err := mk(t, "1.2.0", Release{Version: "1.3.0"}, pol).Check(ctx)
		if err != nil || d.Action != Apply || d.Urgency != Security || pol.calls != 1 || rel.Version != "1.3.0" {
			t.Fatalf("rel=%+v d=%+v err=%v calls=%d", rel, d, err, pol.calls)
		}
	})
	t.Run("default policy stages", func(t *testing.T) {
		_, d, _ := mk(t, "1.2.0", Release{Version: "1.3.0"}, nil).Check(ctx)
		if d.Action != Stage {
			t.Fatalf("d=%+v, want Stage", d)
		}
	})
	t.Run("a reverted version is skipped until a newer one", func(t *testing.T) {
		pol := &countingPolicy{d: Decision{Action: Apply}}
		u := mk(t, "1.2.0", Release{Version: "v1.3.0"}, pol)
		st := stateFile{}
		st.markFailed("1.3.0", "did not confirm", time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC))
		if err := saveState(u.cfg.StateDir, st); err != nil {
			t.Fatal(err)
		}
		_, d, err := u.Check(ctx)
		if err != nil || d.Action != Skip || !strings.Contains(d.Reason, "reverted") || pol.calls != 0 {
			t.Fatalf("d=%+v err=%v calls=%d", d, err, pol.calls)
		}
		u.cfg.Source = staticSource(Release{Version: "1.3.1"})
		if _, d, _ := u.Check(ctx); d.Action != Apply {
			t.Fatalf("a newer release must be offered again, got %+v", d)
		}
	})
	t.Run("source error", func(t *testing.T) {
		u := mk(t, "1.0.0", Release{}, nil)
		u.cfg.Source = SourceFunc(func(context.Context, string) (Release, error) { return Release{}, errors.New("offline") })
		if _, _, err := u.Check(ctx); err == nil || !strings.Contains(err.Error(), "offline") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTornStateIsTreatedAsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := writeFileAtomic(filepath.Join(dir, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := loadState(dir)
	if err != nil || st.Swap != nil || len(st.Failed) != 0 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
}
