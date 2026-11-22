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
