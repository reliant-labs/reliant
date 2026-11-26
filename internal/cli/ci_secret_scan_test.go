package cli

import (
	"strings"
	"testing"
)

// secret-scan asks gitleaks for a leak-specific exit status so a finding is
// told apart from gitleaks failing to run, and maps each mode onto the
// gitleaks verb that scans those commits.
func TestSecretScanArgv(t *testing.T) {
	for _, tc := range []struct {
		name   string
		since  string
		staged bool
		want   string
	}{
		{"history", "", false, "gitleaks detect --source /p --redact --no-banner --verbose --exit-code 9"},
		{"range", "origin/main..HEAD", false, "gitleaks detect --source /p --redact --no-banner --verbose --exit-code 9 --log-opts origin/main..HEAD"},
		{"staged", "", true, "gitleaks protect --source /p --redact --no-banner --verbose --exit-code 9 --staged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := strings.Join(secretScanArgv("gitleaks", "/p", tc.since, tc.staged), " "); got != tc.want {
				t.Errorf("argv:\n got %s\nwant %s", got, tc.want)
			}
		})
	}
}
