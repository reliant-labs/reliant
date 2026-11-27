package cliutil

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// defaultHeartbeatInterval is how long a step may run before Heartbeat first
// speaks.
//
// 15s is under the shortest "no output" cut-off the agent sandboxes forge runs
// in apply (a silent minute reads as a hang), and long enough that a fast
// step, and so a clean `--quiet` run, prints nothing extra.
const defaultHeartbeatInterval = 15 * time.Second

// lineRepeatFactor spaces the repeats of a line-mode heartbeat: after the
// first line at the interval, one every lineRepeatFactor intervals (45s at
// the default). A repeat every 15s printed four lines a minute per step —
// ten for one `forge lint` — while the reader it exists for, an agent with a
// no-output cut-off, needs only a line inside every minute.
const lineRepeatFactor = 3

// statusRefresh is how often a terminal heartbeat redraws its elapsed time.
const statusRefresh = time.Second

// Heartbeat reports that label is still running, on w, until the returned
// stop is called. A step that finishes inside the first 15 seconds prints
// nothing. Output goes to w (stderr at every call site), never stdout: a
// --json document or a verdict line stays alone there.
//
// What it prints depends on what w is:
//
//   - A terminal (IsTerminal): ONE status line, redrawn in place every second
//     and erased when the step stops, so a finished run leaves no trace of
//     it. Every heartbeat in the process shares that line — a nested or
//     concurrent one takes it over and hands it back — so a terminal never
//     shows more than one.
//
//   - Anything else (a pipe, a log, an agent's captured output): a plain line
//     after 15 seconds, then one every 45 seconds — sparse, but never a silent
//     minute.
//
//     stop := cliutil.Heartbeat(os.Stderr, "generate: buf generate")
//     err := run()
//     stop()
//
// stop is idempotent and waits for the reporter to exit, so no line can land
// after the step's own output.
func Heartbeat(w io.Writer, label string) (stop func()) {
	return HeartbeatEvery(w, label, defaultHeartbeatInterval)
}

// HeartbeatEvery is Heartbeat with an explicit first interval, for a caller (a
// test) that cannot wait 15 seconds.
func HeartbeatEvery(w io.Writer, label string, interval time.Duration) (stop func()) {
	return heartbeat(w, label, interval, IsTerminal(w))
}

func heartbeat(w io.Writer, label string, interval time.Duration, tty bool) (stop func()) {
	if w == nil || interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	exited := make(chan struct{})
	var run func()
	if tty {
		b := &termBeat{w: w, label: label, start: time.Now()}
		run = func() {
			defer termStatus.remove(b)
			b.run(done, interval)
		}
	} else {
		run = func() { lineBeat(w, label, interval, done) }
	}
	go func() {
		defer close(exited)
		run()
	}()
	var once sync.Once
	return func() {
		once.Do(func() { close(done) })
		<-exited
	}
}

// lineBeat prints label's heartbeat as plain lines: the first after interval,
// then one every lineRepeatFactor intervals, until done.
func lineBeat(w io.Writer, label string, interval time.Duration, done <-chan struct{}) {
	start := time.Now()
	for n := 0; ; n++ {
		t := time.NewTimer(lineBeatWait(n, interval))
		select {
		case <-done:
			t.Stop()
			return
		case <-t.C:
			fmt.Fprintf(w, "⏳ %s — still running (%s elapsed)\n", label, time.Since(start).Round(time.Second))
		}
	}
}

// lineBeatWait is how long before line n (from 0) of a line-mode heartbeat:
// the interval for the first, lineRepeatFactor intervals for every repeat.
func lineBeatWait(n int, interval time.Duration) time.Duration {
	if n == 0 {
		return interval
	}
	return interval * lineRepeatFactor
}

// termBeat is one terminal heartbeat: invisible until its interval passes,
// then drawn on the shared status line while it is the newest visible one.
type termBeat struct {
	w       io.Writer
	label   string
	start   time.Time
	visible bool
}

func (b *termBeat) run(done <-chan struct{}, interval time.Duration) {
	termStatus.add(b)
	first := time.NewTimer(interval)
	select {
	case <-done:
		first.Stop()
		return
	case <-first.C:
	}
	termStatus.show(b)
	tick := time.NewTicker(statusRefresh)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			termStatus.redraw()
		}
	}
}

func (b *termBeat) text() string {
	return fmt.Sprintf("⏳ %s — still running (%s elapsed)", b.label, time.Since(b.start).Round(time.Second))
}

// statusLine is the single line terminal heartbeats draw on. It is
// process-wide because the terminal is: two heartbeats drawing their own
// lines would each overwrite the other's.
type statusLine struct {
	mu    sync.Mutex
	beats []*termBeat // active, oldest first
	w     io.Writer   // where the drawn line is
	drawn int         // cells it occupies; 0 = nothing drawn
}

var termStatus statusLine

func (s *statusLine) add(b *termBeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beats = append(s.beats, b)
}

func (s *statusLine) show(b *termBeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b.visible = true
	s.redrawLocked()
}

func (s *statusLine) redraw() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.redrawLocked()
}

func (s *statusLine) remove(b *termBeat) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, x := range s.beats {
		if x == b {
			s.beats = append(s.beats[:i], s.beats[i+1:]...)
			break
		}
	}
	s.redrawLocked()
}

// redrawLocked draws the newest visible beat, or erases the line when none
// is. Only "\r" and spaces: no escape sequences, so it works on every
// terminal forge runs in, Windows consoles included.
func (s *statusLine) redrawLocked() {
	var top *termBeat
	for i := len(s.beats) - 1; i >= 0; i-- {
		if s.beats[i].visible {
			top = s.beats[i]
			break
		}
	}
	if s.drawn > 0 && (top == nil || top.w != s.w) {
		fmt.Fprint(s.w, "\r"+strings.Repeat(" ", s.drawn)+"\r")
		s.drawn = 0
	}
	if top == nil {
		return
	}
	text := fitCells(top.text(), terminalWidth(top.w)-1)
	width := cells(text)
	line := "\r" + text
	if pad := s.drawn - width; pad > 0 {
		line += strings.Repeat(" ", pad) + "\r" + text
	}
	fmt.Fprint(top.w, line)
	s.w, s.drawn = top.w, width
}

// cells approximates s's width on a terminal: one cell a rune, two for the
// emoji forge's progress lines start with.
func cells(s string) int {
	n := 0
	for _, r := range s {
		n++
		if r >= 0x1F000 || r == '⏳' || r == '✅' || r == '❌' {
			n++
		}
	}
	return n
}

// fitCells cuts s to at most max cells, ending it with "…" when it was cut,
// so a status line never wraps: a wrapped line cannot be redrawn with "\r".
func fitCells(s string, maxCells int) string {
	if maxCells <= 0 || cells(s) <= maxCells {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		w := cells(string(r))
		if n+w > maxCells-1 {
			break
		}
		b.WriteRune(r)
		n += w
	}
	return b.String() + "…"
}
