package observetest

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/observe"
)

func TestAssertLogCorrelationPassesForTheTraceHandler(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			var h slog.Handler = slog.NewJSONHandler(&buf, nil)
			if format == "text" {
				h = slog.NewTextHandler(&buf, nil)
			}
			AssertLogCorrelation(t, slog.New(observe.NewTraceHandler(h)), buf.String)
		})
	}
}

func TestAssertLogCorrelationFailsWithoutTheTraceHandler(t *testing.T) {
	var buf bytes.Buffer
	ft := &fakeT{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fakeFatal); !ok {
					panic(r)
				}
			}
		}()
		AssertLogCorrelation(ft, slog.New(slog.NewJSONHandler(&buf, nil)), buf.String)
	}()
	if !ft.failed {
		t.Fatal("AssertLogCorrelation passed for a logger with no trace handler")
	}
	if !strings.Contains(strings.Join(ft.msgs, "\n"), "NewTraceHandler") {
		t.Errorf("failure does not name the fix: %v", ft.msgs)
	}
}

func TestParseLineText(t *testing.T) {
	got := parseLine(`time=2026-10-20T00:00:00Z level=INFO msg="a \"quoted\" msg" trace_id=abc n=1`)
	want := map[string]string{"time": "2026-10-20T00:00:00Z", "level": "INFO", "msg": `a "quoted" msg`, "trace_id": "abc", "n": "1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// fakeT records failures instead of failing the real test.
type fakeT struct {
	testing.TB
	failed bool
	msgs   []string
}

func (f *fakeT) Helper() {}
func (f *fakeT) Errorf(format string, args ...any) {
	f.failed = true
	f.msgs = append(f.msgs, fmt.Sprintf(format, args...))
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.Errorf(format, args...)
	panic(fakeFatal{})
}

type fakeFatal struct{}
