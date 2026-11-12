package testwait

import (
	"fmt"
	"strings"
	"testing"
)

// file wraps a test body in a _test.go file importing what the cases use.
func file(body string) string {
	return `package p

import (
	"context"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ = context.Background
var _ = os.Exit
var _ = synctest.Test
var _ = assert.True
var _ = require.True

` + body
}

// got runs CheckFile and renders "rule@line" for each finding, the line
// relative to the first line of body.
func got(t *testing.T, body string) []string {
	t.Helper()
	src := file(body)
	offset := strings.Count(file(""), "\n")
	var out []string
	for _, f := range CheckFile("x_test.go", []byte(src)) {
		out = append(out, fmt.Sprintf("%s@%d", strings.TrimPrefix(f.Rule, "forge-test-"), f.Line-offset))
	}
	return out
}

func TestCheckFile(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		// ── fixed sleeps ─────────────────────────────────────────────
		{"sleep then assert", `func TestX(t *testing.T) {
	go work()
	time.Sleep(100 * time.Millisecond)
	if !done() { t.Fatal("not done") }
}`, []string{"fixed-sleep@3"}},
		{"bare receive on time.After", `func TestX(t *testing.T) {
	<-time.After(50 * time.Millisecond)
	check(t)
}`, []string{"fixed-sleep@2"}},
		{"sleep in a t.Run body", `func TestX(t *testing.T) {
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			time.Sleep(time.Second)
			check(t)
		})
	}
}`, []string{"fixed-sleep@4"}},
		{"sleep in a testing.TB helper", `func waitAndCheck(tb testing.TB) {
	time.Sleep(20 * time.Millisecond)
	check(tb)
}`, []string{"fixed-sleep@2"}},
		{"sleep in a nested if is still followed", `func TestX(t *testing.T) {
	if slow {
		time.Sleep(time.Second)
	}
	check(t)
}`, []string{"fixed-sleep@3"}},
		{"sleep in a final if branch, then an outer assertion", `func TestX(t *testing.T) {
	start()
	if cond {
		time.Sleep(time.Second)
	}
	check(t)
}`, []string{"fixed-sleep@4"}},
		{"sleep in a final switch case, then an outer assertion", `func TestX(t *testing.T) {
	switch mode {
	case "a":
		go work()
		time.Sleep(time.Second)
	}
	check(t)
}`, []string{"fixed-sleep@5"}},
		{"sleep in a final select case, then an outer assertion", `func TestX(t *testing.T) {
	select {
	case <-ch:
		time.Sleep(time.Second)
	default:
	}
	check(t)
}`, []string{"fixed-sleep@4"}},
		{"sleep in an else branch nested two deep, then an outer assertion", `func TestX(t *testing.T) {
	if a {
		start()
	} else {
		if b {
			time.Sleep(time.Second)
		}
	}
	check(t)
}`, []string{"fixed-sleep@6"}},
		{"suite test method", `func (s *Suite) TestX() {
	time.Sleep(time.Second)
	s.check()
}`, []string{"fixed-sleep@2"}},
		{"sleep in a poll loop is the remedy", `func TestX(t *testing.T) {
	for !done() {
		time.Sleep(10 * time.Millisecond)
	}
	check(t)
}`, nil},
		{"sleep in a goroutine trigger", `func TestX(t *testing.T) {
	go func() { time.Sleep(10 * time.Millisecond); close(ch) }()
	<-ch
}`, nil},
		{"sleep in a fake's method", `func (f *fake) Do() error {
	time.Sleep(f.latency)
	return nil
}`, nil},
		{"sleep in a handler literal", `func TestX(t *testing.T) {
	h := func(w, r int) { time.Sleep(time.Second); reply(w) }
	serve(h)
}`, nil},
		{"last statement waits for nothing", `func TestX(t *testing.T) {
	start()
	time.Sleep(100 * time.Millisecond)
}`, nil},
		{"a sleep that aims a cancel is not a wait", `func TestX(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- start(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil { t.Fatal(err) }
}`, nil},
		{"a sleep before some other call still is", `func TestX(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go start(ctx)
	time.Sleep(50 * time.Millisecond)
	stop()
	check(t)
}`, []string{"fixed-sleep@5"}},
		{"a clock tick is not a wait", `func TestX(t *testing.T) {
	a := stamp()
	time.Sleep(time.Millisecond)
	if !stamp().After(a) { t.Fatal("") }
}`, nil},
		{"a helper process exits instead of asserting", `func TestHelperProcess(t *testing.T) {
	time.Sleep(time.Minute)
	os.Exit(0)
}`, nil},
		{"synctest bubble time is fake", `func TestX(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		time.Sleep(time.Minute)
		check(t)
	})
}`, nil},
		{"suppression is the caller's (CheckFile still reports)", `func TestX(t *testing.T) {
	// forge:lint-disable-next-line forge-test-fixed-sleep: negative assertion
	time.Sleep(100 * time.Millisecond)
	check(t)
}`, []string{"fixed-sleep@3"}},

		// ── tight deadlines ──────────────────────────────────────────
		{"select time.After that fails", `func TestX(t *testing.T) {
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}`, []string{"tight-deadline@4"}},
		{"timer variable, from a closure, failing with t.Error", `func TestX(t *testing.T) {
	timeout := time.After(2 * time.Second)
	go func() {
		select {
		case <-done:
		case <-timeout:
			t.Error("timed out")
		}
	}()
}`, []string{"tight-deadline@6"}},
		{"NewTimer .C and a file constant", `const wait = 500 * time.Millisecond
func TestX(t *testing.T) {
	tm := time.NewTimer(wait)
	select {
	case v := <-ch:
		use(v)
	case <-tm.C:
		t.Fatalf("no value")
	}
}`, []string{"tight-deadline@7"}},
		{"context.WithTimeout Done", `func TestX(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	select {
	case <-ch:
	case <-ctx.Done():
		require.FailNow(t, "timed out")
	}
}`, []string{"tight-deadline@6"}},
		{"a suite's s.Fail", `func (s *Suite) TestX() {
	select {
	case <-ch:
	case <-time.After(100 * time.Millisecond):
		s.Fail("timed out")
	}
}`, []string{"tight-deadline@4"}},
		{"a sampling tick in a loop is not a deadline", `func TestX(t *testing.T) {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		case <-time.After(time.Millisecond):
			if got, _ := os.Getwd(); got != cwd {
				t.Fatalf("concurrent child leaked cwd: %s", got)
			}
		}
	}
}`, nil},
		{"a timer case that only logs is not a deadline", `func TestX(t *testing.T) {
	for {
		select {
		case v := <-ch:
			use(v)
		case <-time.After(100 * time.Millisecond):
			t.Logf("still waiting")
		}
	}
}`, nil},
		{"a per-message deadline in a loop still is", `func TestX(t *testing.T) {
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatalf("message %d never arrived", i)
		}
	}
}`, []string{"tight-deadline@5"}},
		{"a deadline that fails inside a plain block still is", `func TestX(t *testing.T) {
	select {
	case <-done:
	case <-time.After(time.Second):
		{
			t.Error("timed out")
			return
		}
	}
}`, []string{"tight-deadline@4"}},
		{"a generous deadline", `func TestX(t *testing.T) {
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}`, nil},
		{"negative assertion: expiry is the pass", `func TestX(t *testing.T) {
	select {
	case <-ch:
		t.Fatal("unexpected send")
	case <-time.After(50 * time.Millisecond):
	}
}`, nil},
		{"a computed deadline", `func TestX(t *testing.T) {
	select {
	case <-done:
	case <-time.After(cfg.Timeout):
		t.Fatal("timed out")
	}
}`, nil},
		{"testify Eventually", `func TestX(t *testing.T) {
	require.Eventually(t, func() bool { return done() }, time.Second, 10*time.Millisecond)
	assert.Eventually(t, func() bool { return done() }, 10*time.Second, 10*time.Millisecond)
}`, []string{"tight-deadline@2"}},
		{"testify Eventually, method form", `func (s *Suite) TestX() {
	s.Require().Eventually(func() bool { return done() }, 2*time.Second, time.Millisecond)
}`, []string{"tight-deadline@2"}},
		{"deadline poll", `func TestX(t *testing.T) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() { return }
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("never")
}`, []string{"tight-deadline@2"}},
		{"inline deadline poll", `func TestX(t *testing.T) {
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if done() { return }
	}
}`, []string{"tight-deadline@2"}},
		{"elapsed poll", `func TestX(t *testing.T) {
	start := time.Now()
	for time.Since(start) < 500*time.Millisecond && !done() {
		time.Sleep(time.Millisecond)
	}
}`, []string{"tight-deadline@3"}},
		{"counted poll", `func TestX(t *testing.T) {
	for i := 0; i < 20; i++ {
		if done() { break }
		time.Sleep(10 * time.Millisecond)
	}
	check(t)
}`, []string{"tight-deadline@2"}},
		{"counted poll, range over int", `func TestX(t *testing.T) {
	for range 50 {
		if done() { return }
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("never")
}`, []string{"tight-deadline@2"}},
		{"counted loop that never leaves early is not a poll", `func TestX(t *testing.T) {
	for i := 0; i < 3; i++ {
		tick()
		time.Sleep(10 * time.Millisecond)
	}
	check(t)
}`, nil},
		{"counted poll whose break leaves an inner switch only", `func TestX(t *testing.T) {
	for i := 0; i < 3; i++ {
		switch i { case 1: break }
		time.Sleep(10 * time.Millisecond)
	}
	check(t)
}`, nil},
		{"a generous deadline poll", `func TestX(t *testing.T) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !done() {
		time.Sleep(10 * time.Millisecond)
	}
}`, nil},
		{"a poll in a handler is behaviour, not a wait", `func TestX(t *testing.T) {
	h := func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) { ping() }
	}
	serve(h)
}`, nil},
		{"a poll in an inline closure runs in the test", `func TestX(t *testing.T) {
	ok := func() bool {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if done() { return true }
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}()
	if !ok { t.Fatal("never") }
}`, []string{"tight-deadline@3"}},
		{"a sleep in a deferred inline closure runs in the test", `func TestX(t *testing.T) {
	defer func() {
		time.Sleep(100 * time.Millisecond)
		check(t)
	}()
	run()
}`, []string{"fixed-sleep@3"}},
		{"a goroutine taking t is not a test body", `func TestX(t *testing.T) {
	go func(t *testing.T) {
		time.Sleep(50 * time.Millisecond)
		close(ch)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) { ping() }
	}(t)
	<-ch
}`, nil},
		{"a goroutine's failing select deadline still flakes", `func TestX(t *testing.T) {
	go func() {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Error("timed out")
		}
	}()
}`, []string{"tight-deadline@5"}},
		{"a closure assigned and passed on is not judged", `func TestX(t *testing.T) {
	sample := func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) { ping() }
	}
	serve(sample)
}`, nil},
		{"a deadline used as data", `func TestX(t *testing.T) {
	exp := time.Now().Add(time.Second)
	for _, c := range cases { mint(c, exp) }
}`, nil},

		// ── wall-clock bounds ────────────────────────────────────────
		{"elapsed upper bound", `func TestX(t *testing.T) {
	start := time.Now()
	run()
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("too slow")
	}
}`, []string{"wall-clock-bound@4"}},
		{"elapsed variable in an if-init, reversed and in a disjunction", `func TestX(t *testing.T) {
	start := time.Now()
	run()
	if elapsed := time.Now().Sub(start); err != nil || time.Second <= elapsed {
		t.Errorf("took %s", elapsed)
	}
}`, []string{"wall-clock-bound@4"}},
		{"testify bounds", `func TestX(t *testing.T) {
	start := time.Now()
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 2*time.Second)
	require.True(t, time.Since(start) < time.Second)
	assert.WithinDuration(t, time.Now(), row.CreatedAt, time.Second)
	assert.WithinDuration(t, want, row.CreatedAt, time.Second)
	assert.Less(t, elapsed, 10*time.Second)
}`, []string{"wall-clock-bound@4", "wall-clock-bound@5", "wall-clock-bound@6"}},
		{"a lower bound cannot flake under load", `func TestX(t *testing.T) {
	start := time.Now()
	retry()
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("did not back off")
	}
}`, nil},
		{"a bound that does not fail the test", `func TestX(t *testing.T) {
	if time.Since(last) > time.Second {
		t.Logf("still waiting")
	}
}`, nil},
		{"a generous bound", `func TestX(t *testing.T) {
	if time.Since(start) > 30*time.Second {
		t.Fatal("hung")
	}
}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := got(t, tc.body)
			if strings.Join(g, ",") != strings.Join(tc.want, ",") {
				t.Errorf("findings = %v, want %v", g, tc.want)
			}
		})
	}
}

func TestCheckFileReadsAnAliasedImport(t *testing.T) {
	src := `package p

import (
	"testing"
	clock "time"
)

func TestX(t *testing.T) {
	clock.Sleep(clock.Second)
	check(t)
}
`
	fs := CheckFile("a_test.go", []byte(src))
	if len(fs) != 1 || fs[0].Rule != RuleFixedSleep || fs[0].Line != 9 {
		t.Fatalf("findings = %+v, want one fixed sleep at line 9", fs)
	}
	if !strings.Contains(fs[0].Message, "time.Sleep(1s)") || !strings.Contains(fs[0].Remediation, "testkit.Eventually") {
		t.Errorf("finding does not name the sleep or point to the fix: %+v", fs[0])
	}
}

func TestCheckFileSkipsNonTestAndGeneratedFiles(t *testing.T) {
	body := file(`func TestX(t *testing.T) {
	time.Sleep(time.Second)
	check(t)
}`)
	if fs := CheckFile("x.go", []byte(body)); len(fs) != 0 {
		t.Errorf("a non-test file: %+v", fs)
	}
	gen := "// Code generated by forge. DO NOT EDIT.\n\n" + body
	if fs := CheckFile("x_test.go", []byte(gen)); len(fs) != 0 {
		t.Errorf("a generated file: %+v", fs)
	}
	if fs := CheckFile("x_test.go", []byte("package p\nfunc (")); len(fs) != 0 {
		t.Errorf("a file that does not parse: %+v", fs)
	}
}
