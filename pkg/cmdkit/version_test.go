package cmdkit

import (
	"bytes"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"
)

// TestPrintVersionReportsEveryStampedField pins that no ldflags-stamped field
// is silently dropped.
//
// The field set is derived by REFLECTING over BuildInfo rather than by listing
// field names here, so adding a field to the struct without teaching
// PrintVersion to print it fails this test instead of shipping a version
// command that quietly omits it. Each field is given a distinctive value so a
// missing one cannot be masked by another field's text.
func TestPrintVersionReportsEveryStampedField(t *testing.T) {
	info := BuildInfo{
		Name:    "sentinel-name",
		Version: "sentinel-version",
		Commit:  "sentinel-commit",
		Date:    "sentinel-date",
	}

	rv := reflect.ValueOf(info)
	if rv.NumField() == 0 {
		t.Fatal("BuildInfo has no fields — nothing to assert")
	}

	var buf bytes.Buffer
	PrintVersion(&buf, info)
	out := buf.String()

	for i := 0; i < rv.NumField(); i++ {
		field := rv.Type().Field(i)
		value := rv.Field(i).String()
		if value == "" {
			t.Fatalf("test fixture left %s empty; every field needs a distinctive value", field.Name)
		}
		if !strings.Contains(out, value) {
			t.Errorf("PrintVersion output omits BuildInfo.%s (%q); a stamped field must not be dropped.\noutput:\n%s",
				field.Name, value, out)
		}
	}
}

// TestPrintVersionReportsTheCompilersOwnGoVersion pins that the Go version
// comes from the build info the compiler embedded, not from anything a caller
// could pass in — a hand-supplied value could disagree with the toolchain that
// actually produced the binary, which makes the whole block untrustworthy.
//
// The expected value is read from the same debug.ReadBuildInfo the producer
// reads, so this asserts the wiring rather than pinning a Go release that will
// change under it. Skips when build info is unavailable, which is also the
// case in which PrintVersion is specified to omit the line.
func TestPrintVersionReportsTheCompilersOwnGoVersion(t *testing.T) {
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.GoVersion == "" {
		t.Skip("no embedded build info in this test binary; the go: line is specified to be absent")
	}

	var buf bytes.Buffer
	PrintVersion(&buf, BuildInfo{Name: "app", Version: "v1", Commit: "abc", Date: "today"})

	if !strings.Contains(buf.String(), bi.GoVersion) {
		t.Errorf("PrintVersion output does not carry the compiler's own Go version (%q).\noutput:\n%s",
			bi.GoVersion, buf.String())
	}
}

// TestPrintVersionLeadsWithTheBinaryName pins the shape a human reads first.
// `<name> <version>` on line one is the line people screenshot into bug
// reports; a block that led with the version alone would not say what was
// being reported.
func TestPrintVersionLeadsWithTheBinaryName(t *testing.T) {
	var buf bytes.Buffer
	PrintVersion(&buf, BuildInfo{Name: "payments", Version: "v2.3.1", Commit: "deadbeef", Date: "2026-01-01"})

	first := strings.SplitN(buf.String(), "\n", 2)[0]
	if first != "payments v2.3.1" {
		t.Errorf("first line = %q, want %q", first, "payments v2.3.1")
	}
}
