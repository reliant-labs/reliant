package gocacheprog

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// client drives a server over io.Pipe the way cmd/go does.
type client struct {
	t    *testing.T
	in   *io.PipeWriter
	dec  *json.Decoder
	next int64
	done chan error
}

func startServer(t *testing.T, cfg Config) *client {
	t.Helper()
	cfg, err := Resolve(cfg, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &client{t: t, in: inW, dec: json.NewDecoder(bufio.NewReader(outR)), done: make(chan error, 1)}
	go func() {
		err := newServer(cfg).serve(inR, outW)
		_ = outW.Close()
		c.done <- err
	}()
	var hello response
	if err := c.dec.Decode(&hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if hello.ID != 0 || strings.Join(hello.KnownCommands, ",") != "get,put,close" {
		t.Fatalf("hello = %+v", hello)
	}
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

// send writes one request (and its base64 body) and reads its response.
func (c *client) send(req request, body []byte) response {
	c.t.Helper()
	c.next++
	req.ID = c.next
	line, _ := json.Marshal(req)
	if _, err := c.in.Write(append(line, '\n')); err != nil {
		c.t.Fatalf("write request: %v", err)
	}
	if req.BodySize > 0 {
		if _, err := fmt.Fprintf(c.in, "%q\n", base64.StdEncoding.EncodeToString(body)); err != nil {
			c.t.Fatalf("write body: %v", err)
		}
	}
	var res response
	if err := c.dec.Decode(&res); err != nil {
		c.t.Fatalf("read response: %v", err)
	}
	if res.ID != req.ID {
		c.t.Fatalf("response id %d for request %d", res.ID, req.ID)
	}
	return res
}

func (c *client) get(aid []byte) response {
	return c.send(request{Command: "get", ActionID: aid}, nil)
}

func (c *client) put(aid, body []byte) response {
	oid := sha256.Sum256(body)
	return c.send(request{Command: "put", ActionID: aid, OutputID: oid[:], BodySize: int64(len(body))}, body)
}

func (c *client) close() {
	c.t.Helper()
	res := c.send(request{Command: "close"}, nil)
	if res.Err != "" {
		c.t.Fatalf("close: %s", res.Err)
	}
	if err := <-c.done; err != nil {
		c.t.Fatalf("Serve: %v", err)
	}
}

func actionID(s string) []byte {
	h := sha256.Sum256([]byte("action:" + s))
	return h[:]
}

// seed writes an entry into dir the way plain `go` would.
func seed(t *testing.T, dir string, aid, body []byte) string {
	t.Helper()
	oid := sha256.Sum256(body)
	d := fileName(dir, oid[:], "d")
	if err := writeAtomic(d, body); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(fileName(dir, aid, "a"), formatIndex(aid, oid[:], int64(len(body)), time.Now())); err != nil {
		t.Fatal(err)
	}
	return d
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// listing is every file under dir with its size and mtime.
func listing(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s %d %d", p, info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func TestGetMiss(t *testing.T) {
	c := startServer(t, Config{Overlay: t.TempDir(), Trusted: t.TempDir()})
	if res := c.get(actionID("x")); !res.Miss || res.Err != "" {
		t.Fatalf("get = %+v, want a miss", res)
	}
	c.close()
}

func TestPutThenGetFromOverlay(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	before := listing(t, trusted)
	c := startServer(t, Config{Overlay: overlay, Trusted: trusted})
	body := []byte("compiled package bytes")
	res := c.put(actionID("a"), body)
	if res.Err != "" || !strings.HasPrefix(res.DiskPath, overlay+string(filepath.Separator)) {
		t.Fatalf("put = %+v, want a path in the overlay", res)
	}
	if got := readFile(t, res.DiskPath); got != string(body) {
		t.Fatalf("put wrote %q", got)
	}
	got := c.get(actionID("a"))
	oid := sha256.Sum256(body)
	if got.Miss || got.DiskPath != res.DiskPath || got.Size != int64(len(body)) || string(got.OutputID) != string(oid[:]) || got.Time == nil {
		t.Fatalf("get = %+v", got)
	}
	c.close()
	if after := listing(t, trusted); after != before {
		t.Fatalf("trusted changed:\n%s\n---\n%s", before, after)
	}
}

func TestTrustedHitIsReturnedInPlace(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	body := []byte("main's bytes")
	d := seed(t, trusted, actionID("t"), body)
	a := fileName(trusted, actionID("t"), "a")
	before := readFile(t, a)
	c := startServer(t, Config{Overlay: overlay, Trusted: trusted})
	res := c.get(actionID("t"))
	if res.Miss || res.DiskPath != d {
		t.Fatalf("get = %+v, want the trusted path %s", res, d)
	}
	c.close()
	if readFile(t, a) != before || readFile(t, d) != string(body) {
		t.Fatal("a trusted hit changed the trusted entry's bytes")
	}
	if entries, _ := os.ReadDir(overlay); len(entries) != 0 {
		t.Fatalf("a trusted hit wrote to the overlay: %v", entries)
	}
}

func TestTrustedHitRestampsStaleEntry(t *testing.T) {
	trusted := t.TempDir()
	d := seed(t, trusted, actionID("t"), []byte("main's bytes"))
	a := fileName(trusted, actionID("t"), "a")
	old := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{a, d} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	c := startServer(t, Config{Overlay: t.TempDir(), Trusted: trusted})
	if res := c.get(actionID("t")); res.Miss {
		t.Fatal("miss")
	}
	c.close()
	for _, p := range []string{a, d} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(info.ModTime()) > time.Minute {
			t.Errorf("%s was not restamped (mtime %s)", p, info.ModTime())
		}
	}
}

func TestReadOnlyTrustedDirStillHits(t *testing.T) {
	trusted := t.TempDir()
	d := seed(t, trusted, actionID("ro"), []byte("main's bytes"))
	a := fileName(trusted, actionID("ro"), "a")
	old := time.Now().Add(-3 * time.Hour)
	for _, p := range []string{a, d} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o444); err != nil {
			t.Fatal(err)
		}
	}
	shard := filepath.Dir(d)
	for _, dir := range []string{shard, filepath.Dir(a), trusted} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, dir := range []string{trusted, shard, filepath.Dir(a)} {
			_ = os.Chmod(dir, 0o755)
		}
	})
	c := startServer(t, Config{Overlay: t.TempDir(), Trusted: trusted})
	res := c.get(actionID("ro"))
	if res.Miss || res.Err != "" || res.DiskPath != d {
		t.Fatalf("get from a read-only trusted dir = %+v, want a hit at %s", res, d)
	}
	c.close()
}

func TestTrustedWinsOverOverlay(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	td := seed(t, trusted, actionID("both"), []byte("trusted"))
	seed(t, overlay, actionID("both"), []byte("overlay"))
	c := startServer(t, Config{Overlay: overlay, Trusted: trusted})
	if res := c.get(actionID("both")); res.DiskPath != td {
		t.Fatalf("get = %+v, want the trusted entry", res)
	}
	c.close()
}

func TestPutDedupsAgainstTrusted(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	body := []byte("same bytes, another action")
	td := seed(t, trusted, actionID("main"), body)
	before := listing(t, trusted)
	c := startServer(t, Config{Overlay: overlay, Trusted: trusted})
	res := c.put(actionID("pr"), body)
	if res.Err != "" || res.DiskPath != td {
		t.Fatalf("put = %+v, want the trusted output %s", res, td)
	}
	oid := sha256.Sum256(body)
	if _, err := os.Stat(fileName(overlay, oid[:], "d")); !os.IsNotExist(err) {
		t.Fatalf("a deduplicated put copied the output into the overlay (%v)", err)
	}
	// The overlay's index points at the trusted bytes.
	if got := c.get(actionID("pr")); got.Miss || got.DiskPath != td {
		t.Fatalf("get = %+v, want the trusted output via the overlay index", got)
	}
	c.close()
	if after := listing(t, trusted); after != before {
		t.Fatalf("trusted changed:\n%s\n---\n%s", before, after)
	}
}

func TestPutRejectsWrongOutputID(t *testing.T) {
	c := startServer(t, Config{Overlay: t.TempDir(), Trusted: t.TempDir()})
	bad := sha256.Sum256([]byte("something else"))
	res := c.send(request{Command: "put", ActionID: actionID("p"), OutputID: bad[:], BodySize: 4}, []byte("body"))
	if res.Err == "" {
		t.Fatalf("put with a wrong output id = %+v, want an error", res)
	}
	// The process keeps serving.
	if res := c.get(actionID("p")); !res.Miss {
		t.Fatalf("get after a rejected put = %+v, want a miss", res)
	}
	c.close()
}

func TestCorruptIndexIsAMiss(t *testing.T) {
	trusted := t.TempDir()
	seed(t, trusted, actionID("c"), []byte("bytes"))
	a := fileName(trusted, actionID("c"), "a")
	good := readFile(t, a)
	for name, content := range map[string]string{
		"truncated":    good[:len(good)-5],
		"too long":     good + "x",
		"bad header":   "v2" + good[2:],
		"other action": string(formatIndex(actionID("other"), make([]byte, 32), 5, time.Now())),
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			if err := writeAtomic(a, []byte(content)); err != nil {
				t.Fatal(err)
			}
			c := startServer(t, Config{Overlay: t.TempDir(), Trusted: trusted})
			if res := c.get(actionID("c")); !res.Miss {
				t.Fatalf("get over a %s index = %+v, want a miss", name, res)
			}
			c.close()
		})
	}
}

func TestSizeMismatchFallsThroughToOverlay(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	td := seed(t, trusted, actionID("s"), []byte("full output"))
	if err := writeAtomic(td, []byte("trunc")); err != nil {
		t.Fatal(err)
	}
	c := startServer(t, Config{Overlay: overlay, Trusted: trusted})
	if res := c.get(actionID("s")); !res.Miss {
		t.Fatalf("get over a truncated trusted output = %+v, want a miss", res)
	}
	od := seed(t, overlay, actionID("s"), []byte("full output"))
	if res := c.get(actionID("s")); res.Miss || res.DiskPath != od {
		t.Fatalf("get = %+v, want the overlay entry %s", res, od)
	}
	c.close()
}

func TestOverlayHitRestampsStaleEntries(t *testing.T) {
	overlay := t.TempDir()
	d := seed(t, overlay, actionID("r"), []byte("bytes"))
	a := fileName(overlay, actionID("r"), "a")
	old := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{a, d} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	c := startServer(t, Config{Overlay: overlay, Trusted: t.TempDir()})
	if res := c.get(actionID("r")); res.Miss {
		t.Fatal("miss")
	}
	c.close()
	for _, p := range []string{a, d} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if time.Since(info.ModTime()) > time.Minute {
			t.Errorf("%s was not restamped (mtime %s)", p, info.ModTime())
		}
	}
}

func TestMissingTrustedRunsOnOverlay(t *testing.T) {
	overlay := t.TempDir()
	cfg, err := Resolve(Config{Overlay: overlay, Trusted: filepath.Join(t.TempDir(), "absent")}, func(string) string { return "" })
	if err != nil {
		t.Fatalf("Resolve with a missing trusted dir: %v", err)
	}
	if cfg.Trusted != "" || cfg.TrustedNote == "" {
		t.Fatalf("cfg = %+v, want overlay-only with a note", cfg)
	}
	c := startServer(t, Config{Overlay: overlay, Trusted: filepath.Join(t.TempDir(), "absent")})
	if res := c.put(actionID("m"), []byte("x")); res.Err != "" {
		t.Fatalf("put = %+v", res)
	}
	if res := c.get(actionID("m")); res.Miss {
		t.Fatal("miss after put")
	}
	c.close()
}

func TestOverlayEqualToTrustedIsRefused(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, overlay := range []string{dir, dir + string(filepath.Separator), link} {
		if _, err := Resolve(Config{Overlay: overlay, Trusted: dir}, func(string) string { return "" }); err == nil {
			t.Errorf("Resolve(overlay %s, trusted %s) succeeded, want a refusal", overlay, dir)
		}
	}
}

func TestResolveFromEnvironment(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	env := map[string]string{"GOCACHE": overlay, TrustedEnv: trusted, StatsEnv: "/x/stats"}
	cfg, err := Resolve(Config{}, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Overlay != overlay || cfg.Trusted != trusted || cfg.StatsPath != "/x/stats" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if _, err := Resolve(Config{}, func(string) string { return "" }); err == nil {
		t.Fatal("Resolve with no overlay succeeded")
	}
}

func TestBase64BodyAndEmptyBody(t *testing.T) {
	overlay := t.TempDir()
	c := startServer(t, Config{Overlay: overlay, Trusted: t.TempDir()})
	body := make([]byte, 70000) // spans base64 line-free encoding of a large object
	for i := range body {
		body[i] = byte(i * 7)
	}
	res := c.put(actionID("big"), body)
	if res.Err != "" || readFile(t, res.DiskPath) != string(body) {
		t.Fatalf("put of a large body = %+v", res)
	}
	empty := c.put(actionID("empty"), nil)
	if empty.Err != "" {
		t.Fatalf("put of an empty body = %+v", empty)
	}
	if got := c.get(actionID("empty")); got.Miss || got.Size != 0 {
		t.Fatalf("get of an empty output = %+v", got)
	}
	c.close()
}

func TestStatsLine(t *testing.T) {
	overlay, trusted := t.TempDir(), t.TempDir()
	stats := filepath.Join(t.TempDir(), "stats.jsonl")
	seed(t, trusted, actionID("t"), []byte("main"))
	c := startServer(t, Config{Overlay: overlay, Trusted: trusted, StatsPath: stats})
	c.get(actionID("t"))
	c.get(actionID("none"))
	c.put(actionID("p"), []byte("main")) // deduplicated
	c.get(actionID("p"))
	c.close()
	var line statsLine
	if err := json.Unmarshal([]byte(readFile(t, stats)), &line); err != nil {
		t.Fatal(err)
	}
	if line.Gets != 3 || line.TrustedHits != 1 || line.OverlayHits != 1 || line.Misses != 1 || line.Puts != 1 || line.DedupedPuts != 1 || line.PutBytes != 4 {
		t.Fatalf("stats = %+v", line)
	}
}

func TestUnknownCommandIsAnErrorNotAnExit(t *testing.T) {
	c := startServer(t, Config{Overlay: t.TempDir(), Trusted: t.TempDir()})
	if res := c.send(request{Command: "frob"}, nil); res.Err == "" {
		t.Fatalf("frob = %+v, want an error", res)
	}
	if res := c.get(make([]byte, 3)); res.Err == "" {
		t.Fatalf("get with a short id = %+v, want an error", res)
	}
	c.close()
}
