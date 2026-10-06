package catalog

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// Discovery reruns on every catalog read, so the shadowing warning must fire
// once per (skill, winner, loser) per process, not once per pass.
func TestReportShadowed_WarnsOncePerShadowing(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	shadow := ShadowedSkill{
		Key:        "report-shadowed-test/" + t.Name(),
		WinnerPath: "/winner/SKILL.md",
		LoserPath:  "/loser/SKILL.md",
	}
	for range 5 {
		reportShadowed([]ShadowedSkill{shadow})
	}
	if n := strings.Count(buf.String(), "skill delivered by two producers"); n != 1 {
		t.Fatalf("warned %d times, want 1:\n%s", n, buf.String())
	}

	// A different loser is a different fact and is reported.
	shadow.LoserPath = "/other-loser/SKILL.md"
	reportShadowed([]ShadowedSkill{shadow})
	if n := strings.Count(buf.String(), "skill delivered by two producers"); n != 2 {
		t.Fatalf("warned %d times after a new shadowing, want 2", n)
	}
}
