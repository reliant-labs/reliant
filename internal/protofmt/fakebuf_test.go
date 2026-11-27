package protofmt

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeBufEnv makes the test binary act as `buf format <file>`, so the session
// logic is tested on every OS with no real buf. The fake's "formatter"
// collapses runs of spaces in every line; a file containing "BROKEN" does not
// parse and exits non-zero, the way buf rejects a bad proto.
const fakeBufEnv = "FORGE_PROTOFMT_FAKE_BUF"

func TestMain(m *testing.M) {
	if os.Getenv(fakeBufEnv) == "1" && len(os.Args) == 3 && os.Args[1] == "format" {
		os.Exit(fakeBufMain(os.Args[2]))
	}
	os.Exit(m.Run())
}

func fakeBufMain(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if strings.Contains(string(b), "BROKEN") {
		fmt.Fprintln(os.Stderr, "syntax error: BROKEN")
		return 1
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	fmt.Print(strings.Join(lines, "\n"))
	return 0
}

// useFakeBuf points the session's buf resolution at this test binary.
func useFakeBuf(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeBufEnv, "1")
	old := resolveBin
	resolveBin = func(context.Context, string) (string, error) { return exe, nil }
	t.Cleanup(func() { resolveBin = old })
}

const bufYAML = "version: v2\nmodules:\n  - path: proto\n"
