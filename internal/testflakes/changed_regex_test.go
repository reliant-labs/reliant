package testflakes

import (
	"fmt"
	"regexp"
	"testing"
)

func TestRunRegexLargeSelectionKeepsEveryTest(t *testing.T) {
	var names []string
	for i := range 5000 {
		names = append(names, fmt.Sprintf("TestLargePackage_%04d", i))
	}
	names = append(names, "FuzzParse")
	changed := ChangedTests{Packages: []ChangedPackage{{Rel: "internal/cli", Names: names}}}
	run := changed.RunRegex()
	if len(run) > 8192 {
		t.Fatalf("run argument has %d bytes", len(run))
	}
	re := regexp.MustCompile(run)
	for _, name := range names {
		if !re.MatchString(name) {
			t.Errorf("run regex omits %s", name)
		}
	}
	if re.MatchString("ExampleUnrelated") {
		t.Fatal("run regex selected a non-test declaration")
	}
}
