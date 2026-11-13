package pgtest

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func occupiedBootPort(t *testing.T) (uint32, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return uint32(ln.Addr().(*net.TCPAddr).Port), ln
}

func bootDirs(t *testing.T) func(uint32) string {
	t.Helper()
	root := t.TempDir()
	return func(p uint32) string { return filepath.Join(root, fmt.Sprint(p)) }
}

func assertBootListenerAlive(t *testing.T, ln net.Listener) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 1e9)
	if err != nil {
		t.Fatalf("unrelated listener was stopped: %v", err)
	}
	_ = conn.Close()
}

func TestEmbeddedBootRetriesOccupiedCandidate(t *testing.T) {
	port, ln := occupiedBootPort(t)
	dirFor := bootDirs(t)
	collision := fmt.Errorf("process already listening on port %d", port)
	want := &Embedded{}
	cfg := embeddedpostgres.DefaultConfig().Version("16.9.0").CachePath("cached-binaries").
		Username("forge").Password("forge").Database("postgres").Locale("C").Encoding("UTF8").
		StartParameters(startParameters("linux"))
	reserved, starts := 0, 0
	ep, gotPort, claim, err := startEmbeddedOnFreshPort(cfg, func() (uint32, error) {
		reserved++
		return port + uint32(reserved-1), nil
	}, dirFor, func(got embeddedpostgres.Config) (*Embedded, error) {
		starts++
		p := port + uint32(starts-1)
		if !reflect.DeepEqual(got, cfg.Port(p).RuntimePath(dirFor(p)).Logger(bootLog{path: bootLogPath(dirFor(p))})) {
			t.Fatal("boot changed configuration beyond port/runtime path/boot log")
		}
		if starts == 1 {
			return nil, collision
		}
		return want, nil
	})
	if err != nil || ep != want || gotPort != port+1 || reserved != 2 || starts != 2 {
		t.Fatalf("got ep=%p port=%d err=%v reserves=%d starts=%d", ep, gotPort, err, reserved, starts)
	}
	if bootClaimHeld(dirFor(port)) {
		t.Fatal("failed attempt retained its boot lock")
	}
	if info, err := os.Stat(bootClaimPath(dirFor(port))); err != nil || info.Size() != 0 {
		t.Fatalf("failed attempt must leave one reusable zero-byte mutex: %v %v", info, err)
	}
	claim.release()
	if _, err := os.Stat(dirFor(port)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty failed claim left behind: %v", err)
	}
	if bootClaimHeld(dirFor(port + 1)) {
		t.Fatal("released successful claim is still locked")
	}
	if info, err := os.Stat(bootClaimPath(dirFor(port + 1))); err != nil || info.Size() != 0 {
		t.Fatalf("released claim must preserve reusable mutex inode: %v %v", info, err)
	}
	assertBootListenerAlive(t, ln)
}

func TestEmbeddedBootCollisionExhaustion(t *testing.T) {
	dirFor := bootDirs(t)
	var ports []uint32
	var listeners []net.Listener
	for range embeddedBootAttempts {
		p, ln := occupiedBootPort(t)
		ports, listeners = append(ports, p), append(listeners, ln)
	}
	reserves, starts := 0, 0
	var last error
	ep, port, claim, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		p := ports[reserves]
		reserves++
		return p, nil
	}, dirFor, func(embeddedpostgres.Config) (*Embedded, error) {
		last = fmt.Errorf("process already listening on port %d", ports[starts])
		starts++
		return nil, last
	})
	var exhausted *portBootError
	if ep != nil || port != 0 || claim != nil || !errors.As(err, &exhausted) || !errors.Is(err, last) || starts != embeddedBootAttempts || reserves != embeddedBootAttempts {
		t.Fatalf("got ep=%p port=%d err=%v starts=%d reserves=%d", ep, port, err, starts, reserves)
	}
	if !reflect.DeepEqual(exhausted.ports, ports) || !strings.Contains(err.Error(), EnvBaseURL) {
		t.Fatalf("not actionable: %v", err)
	}
	// Every collision releases its lock. Stable per-port mutex inodes stay:
	// never unlink an inode a contender might already have open.
	if mutexes, _ := filepath.Glob(filepath.Join(filepath.Dir(dirFor(ports[0])), "*.boot")); len(mutexes) != len(ports) {
		t.Fatalf("want one bounded reusable mutex per candidate, got %v", mutexes)
	}
	for _, p := range ports {
		if bootClaimHeld(dirFor(p)) {
			t.Fatalf("collision retained boot lock for %d", p)
		}
	}
	for _, ln := range listeners {
		assertBootListenerAlive(t, ln)
	}
}

// unbindablePort is outside the TCP port range: net.Listen refuses it with an
// error that is not EADDRINUSE.
const unbindablePort = 70000

// A collision that cleared before pgtest re-bound the port (the winning
// listener already closed) is still a collision: upstream's precheck lost a
// race, so the boot moves to a fresh candidate rather than failing
// (forge #1932, #1942).
func TestEmbeddedBootRetriesACollisionThatCleared(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	cleared := uint32(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()
	next, _ := occupiedBootPort(t) // any distinct candidate; start is faked
	dirFor := bootDirs(t)
	want := &Embedded{}
	candidates := []uint32{cleared, next}
	reserves, starts := 0, 0
	ep, gotPort, claim, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		p := candidates[reserves]
		reserves++
		return p, nil
	}, dirFor, func(embeddedpostgres.Config) (*Embedded, error) {
		starts++
		if starts == 1 {
			return nil, fmt.Errorf("process already listening on port %d", cleared)
		}
		return want, nil
	})
	if err != nil || ep != want || gotPort != next || reserves != 2 || starts != 2 {
		t.Fatalf("got ep=%p port=%d err=%v reserves=%d starts=%d, want a retry on %d", ep, gotPort, err, reserves, starts, next)
	}
	claim.release()
	if bootClaimHeld(dirFor(cleared)) {
		t.Fatal("the cleared candidate retained its boot lock")
	}
}

func TestEmbeddedBootDoesNotRetryOtherFailures(t *testing.T) {
	for _, cause := range []error{
		&FetchError{URL: "https://example.invalid/postgres.txz", Status: 503, Attempts: 6},
		os.ErrPermission,
		errors.New("initdb: No space left on device"),
		errors.New("could not start postgres using pg_ctl: initdb failed"),
		// The collision wording, but a re-bind fails with something other
		// than EADDRINUSE (here: a port no socket can have), so it was no
		// collision at all.
		fmt.Errorf("process already listening on port %d", unbindablePort),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			starts, reserves := 0, 0
			_, _, _, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
				reserves++
				return unbindablePort, nil
			}, bootDirs(t), func(embeddedpostgres.Config) (*Embedded, error) {
				starts++
				return nil, cause
			})
			if !errors.Is(err, cause) || starts != 1 || reserves != 1 {
				t.Fatalf("err=%v starts=%d reserves=%d, want original error once", err, starts, reserves)
			}
		})
	}
}

func TestEmbeddedBootReserveFailure(t *testing.T) {
	cause := errors.New("socket budget exhausted")
	starts, reserves := 0, 0
	_, _, _, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		reserves++
		return 0, cause
	}, bootDirs(t), func(embeddedpostgres.Config) (*Embedded, error) {
		starts++
		return nil, nil
	})
	if !errors.Is(err, cause) || reserves != 1 || starts != 0 {
		t.Fatalf("err=%v reserves=%d starts=%d", err, reserves, starts)
	}
}

func TestEmbeddedBootSkipsForeignRuntimeDirectory(t *testing.T) {
	port, ln := occupiedBootPort(t)
	dirFor := bootDirs(t)
	if err := os.MkdirAll(dirFor(port), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dirFor(port), "foreign-data")
	if err := os.WriteFile(marker, []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	reserves, starts := 0, 0
	_, gotPort, claim, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		reserves++
		return port + uint32(reserves-1), nil
	}, dirFor, func(embeddedpostgres.Config) (*Embedded, error) {
		starts++
		return &Embedded{}, nil
	})
	if err != nil || gotPort != port+1 || starts != 1 || reserves != 2 {
		t.Fatalf("port=%d err=%v starts=%d reserves=%d", gotPort, err, starts, reserves)
	}
	claim.release()
	if b, err := os.ReadFile(marker); err != nil || string(b) != "not ours" {
		t.Fatalf("foreign data changed: %q %v", b, err)
	}
	assertBootListenerAlive(t, ln)
}

func TestEmbeddedBootPreservesUncertainPartialStart(t *testing.T) {
	p, ln := occupiedBootPort(t)
	dirFor := bootDirs(t)
	starts := 0
	cause := fmt.Errorf("process already listening on port %d", p)
	pidFile := filepath.Join(dirFor(p), "data", "postmaster.pid")
	_, _, _, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		return p, nil
	}, dirFor, func(embeddedpostgres.Config) (*Embedded, error) {
		starts++
		if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pidFile, []byte("123\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return nil, cause
	})
	if !errors.Is(err, cause) || starts != 1 {
		t.Fatalf("err=%v starts=%d", err, starts)
	}
	if b, err := os.ReadFile(pidFile); err != nil || string(b) != "123\n" {
		t.Fatalf("partial boot data removed: %q %v", b, err)
	}
	assertBootListenerAlive(t, ln)
}

func TestEmbeddedBindCollisionDiagnostics(t *testing.T) {
	bind := "could not start postgres using pg_ctl start:\nLOG:  could not bind IPv4 address \"127.0.0.1\": Address already in use\nFATAL:  could not create any TCP/IP sockets"
	for _, tc := range []struct {
		name, message string
		want          bool
	}{
		{"postgres bind", bind, true},
		{"windows bind", strings.ReplaceAll(bind, "Address already in use", "Only one usage of each socket address (protocol/network address/port) is normally permitted."), true},
		{"bind permission", strings.ReplaceAll(bind, "Address already in use", "Permission denied"), false},
		{"unrelated log", "download failed: " + bind, false},
		{"nonfatal ipv6 bind", strings.ReplaceAll(bind, "FATAL:  could not create any TCP/IP sockets", "server started"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := embeddedBindCollision(errors.New(tc.message), 0, t.TempDir()); got != tc.want {
				t.Fatalf("collision=%v, want %v", got, tc.want)
			}
		})
	}
}

// The claim must exclude a contender for the whole boot, even though the
// dependency RemoveAlls RuntimePath first, and must outlive start: the caller
// releases it only once the server is owned.
func TestEmbeddedBootClaimSurvivesRuntimeRemoval(t *testing.T) {
	port, _ := occupiedBootPort(t)
	dirFor := bootDirs(t)
	dir := dirFor(port)
	want := &Embedded{}
	ep, gotPort, claim, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		return port, nil
	}, dirFor, func(embeddedpostgres.Config) (*Embedded, error) {
		_ = os.RemoveAll(dir) // what StartEmbedded does before extracting
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		if !bootClaimHeld(dir) {
			t.Fatal("boot claim not held while starting")
		}
		if c, err := claimBootDir(dir); c != nil || err != nil {
			t.Fatalf("contender claimed a dir mid-boot: %v %v", c, err)
		}
		return want, nil
	})
	if err != nil || ep != want || gotPort != port || claim == nil {
		t.Fatalf("ep=%p port=%d claim=%v err=%v", ep, gotPort, claim, err)
	}
	if !bootClaimHeld(dir) {
		t.Fatal("boot claim released before the caller established ownership")
	}
	claim.release()
	if bootClaimHeld(dir) {
		t.Fatal("boot claim still held after release")
	}
	if c, err := claimBootDir(dir); c != nil || err != nil {
		t.Fatalf("contender claimed a booted server's dir: %v %v", c, err)
	}
}

func TestEmbeddedBootSkipsClaimedCandidate(t *testing.T) {
	port, ln := occupiedBootPort(t)
	dirFor := bootDirs(t)
	if err := os.MkdirAll(filepath.Dir(dirFor(port)), 0o755); err != nil {
		t.Fatal(err)
	}
	other, err := claimBootDir(dirFor(port))
	if err != nil || other == nil {
		t.Fatalf("claim: %v %v", other, err)
	}
	defer other.release()
	reserves, starts := 0, 0
	_, gotPort, claim, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		reserves++
		return port + uint32(reserves-1), nil
	}, dirFor, func(embeddedpostgres.Config) (*Embedded, error) {
		starts++
		return &Embedded{}, nil
	})
	if err != nil || gotPort != port+1 || starts != 1 || reserves != 2 {
		t.Fatalf("port=%d err=%v starts=%d reserves=%d", gotPort, err, starts, reserves)
	}
	claim.release()
	assertBootListenerAlive(t, ln)
}
