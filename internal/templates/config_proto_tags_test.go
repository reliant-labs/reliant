package templates

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// The scaffolded config.proto declares its fields by topic, not by number, so
// its comment names the highest number in use. It must stay true when a field
// is added to the template, or "append the next number" collides in buf.
func TestConfigProtoStatesItsHighestFieldNumber(t *testing.T) {
	raw, err := os.ReadFile("project/config.proto.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	stated := regexp.MustCompile(`The highest number\s+// forge scaffolds is (\d+)`).FindSubmatch(raw)
	if stated == nil {
		t.Fatal("config.proto.tmpl no longer states its highest field number")
	}
	want, _ := strconv.Atoi(string(stated[1]))
	highest := 0
	for _, m := range regexp.MustCompile(`(?m)^  \S+ \S+ = (\d+) \[`).FindAllSubmatch(raw, -1) {
		if n, _ := strconv.Atoi(string(m[1])); n > highest {
			highest = n
		}
	}
	if highest != want {
		t.Fatalf("config.proto.tmpl says its highest field number is %d, but AppConfig uses %d: update the comment", want, highest)
	}
}

// A sensitive field never gets a CLI flag: the loader registers none for it
// (a secret in argv leaks through ps and shell history), so a `flag:` on one
// documents a flag the binary does not have. The scaffold once declared
// `flag: "database-url"`, and db.go read that phantom flag (friction #26).
func TestConfigProtoDeclaresNoFlagOnASensitiveField(t *testing.T) {
	raw, err := os.ReadFile("project/config.proto.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	fields := regexp.MustCompile(`(?ms)^  \S+ (\S+) = \d+ \[\(forge\.v1\.config\) = \{(.*?)\n  \}\];`).FindAllSubmatch(raw, -1)
	if len(fields) < 10 {
		t.Fatalf("found %d annotated fields in config.proto.tmpl; the pattern no longer matches its layout", len(fields))
	}
	sensitive := 0
	for _, m := range fields {
		body := string(m[2])
		if !regexp.MustCompile(`(?m)^\s*sensitive: true`).MatchString(body) {
			continue
		}
		sensitive++
		if regexp.MustCompile(`(?m)^\s*flag:`).MatchString(body) {
			t.Errorf("config.proto.tmpl: sensitive field %s declares a flag, which is never registered: delete it", m[1])
		}
	}
	if sensitive == 0 {
		t.Fatal("found no sensitive field in config.proto.tmpl; database_url at least is one")
	}
}
