package deploytarget

import (
	"strings"
	"testing"
)

// composeUnsetVarWarning is what compose prints, on the stderr forge
// captures with stdout, for a compose file that reads an unset ${VAR}.
const composeUnsetVarWarning = `time="2026-11-20T09:00:00Z" level=warning msg="The \"API_KEY\" variable is not set. Defaulting to a blank string."`

// TestComposeContainerIDsSkipsDiagnostics pins NS FG-17: an unset ${VAR}
// warning beside `ps --quiet` output is not a container.
func TestComposeContainerIDsSkipsDiagnostics(t *testing.T) {
	short, full := "0123456789ab", strings.Repeat("a1", 32)
	// One id per line, as compose prints them; the warnings have spaces.
	out := []byte(composeUnsetVarWarning + "\n" + short + "\n" + full + "\nWARN[0000] The \"X\" variable is not set.\n")
	got := composeContainerIDs(out)
	if strings.Join(got, ",") != short+","+full {
		t.Fatalf("ids = %q, want just the two container ids", got)
	}
}

// The JSON `ps` reader skips the same diagnostics, in both of compose's
// output shapes (one object per line, or one array).
func TestParseComposePSSkipsDiagnostics(t *testing.T) {
	for name, raw := range map[string]string{
		"ndjson": composeUnsetVarWarning + "\n" + `{"Service":"db","State":"running"}` + "\n",
		"array":  composeUnsetVarWarning + "\n" + `[{"Service":"db","State":"running"}]` + "\n",
		"pretty array": composeUnsetVarWarning + "\n[\n" +
			`  {"Service":"db","State":"running"}` + "\n]\nWARN[0000] warning after output\n",
	} {
		got, err := parseComposePS([]byte(raw))
		if err != nil || len(got) != 1 {
			t.Errorf("%s: containers = %+v, err = %v; want the one container", name, got, err)
		}
	}
}

func TestParseComposePSRejectsInvalidJSON(t *testing.T) {
	for _, raw := range []string{`[{"Service":"db"}`, "{\"Service\": \"db\",\n", "unrecognized output\n", "[\nmalformed record\n]\n",
		`{"level":"warning","msg":"unknown output"}`, `[null]`} {
		if got, err := parseComposePS([]byte(raw)); err == nil {
			t.Errorf("parseComposePS(%q) = %+v, want an error", raw, got)
		}
	}
}
