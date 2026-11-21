package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidatePublicGitHubMetadataFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the public API shell adapter with fixture curl; runs in task test")
	}
	requireCICommands(t, "bash", "jq")
	for _, tc := range []struct {
		name, mode, endpoint, token string
		args                        []string
		want                        string
		exit                        int
		sleeps                      string // the waits the adapter took, one per line
	}{
		{"live PR fields", "", "pulls/7", "", nil, `"body":"proof"`, 0, ""},
		{"compat label query", "", "issues/7/labels", "", []string{"--jq", `[.[] | select(.name == "breaking:approved")] | length`}, "1", 0, ""},
		{"compat actor query yields unquoted login", "", "issues/7/events", "", []string{"--jq", `.[] | .actor.login`}, "\nlater\n", 0, ""},
		{"every event page", "", "issues/7/events", "", nil, `"login":"later"`, 0, ""},
		{"the workflow token authenticates on stdin", "", "pulls/7", "workflow-token", nil, `"body":"proof"`, 0, ""},
		// forge#2459: a rate limit is retried when GitHub's retry time fits the
		// budget, and otherwise reported as infrastructure (exit 75).
		{"retry after Retry-After", "retry-after", "pulls/7", "", nil, `"body":"proof"`, 0, "3\n"},
		{"retry at X-RateLimit-Reset", "reset-soon", "pulls/7", "", nil, `"body":"proof"`, 0, "SOON\n"},
		{"secondary rate limit backs off", "secondary", "pulls/7", "", nil, `"body":"proof"`, 0, "2\n"},
		{"a reset beyond the budget is not waited for", "reset-late", "pulls/7", "", nil,
			"infrastructure: GitHub API rate limit (HTTP 403) reading repos/reliant-labs/forge/pulls/7 after 1 attempt(s); the limit resets at", 75, ""},
		{"do not trust partial history", "rate-limit", "issues/7/events", "", nil, "infrastructure: GitHub API rate limit (HTTP 403)", 75, ""},
		{"a persistent 5xx exhausts its retries", "502", "pulls/7", "", nil, "infrastructure: GitHub API error (HTTP 502) reading repos/reliant-labs/forge/pulls/7 after 4 attempt(s)", 75, "2\n4\n8\n"},
		{"an unreachable API exhausts its retries", "unreachable", "pulls/7", "", nil, "infrastructure: GitHub API unreachable (curl: (7) Failed to connect", 75, "2\n4\n8\n"},
		{"a 404 is not retried", "404", "pulls/7", "", nil, "GitHub API returned HTTP 404 reading repos/reliant-labs/forge/pulls/7: Not Found", 1, ""},
		{"a permission 403 is not a rate limit", "forbidden", "pulls/7", "", nil, "GitHub API returned HTTP 403 reading repos/reliant-labs/forge/pulls/7: Resource not accessible", 1, ""},
		{"reject malformed labels response", "malformed", "issues/7/labels", "", nil, "", 1, ""},
		{"reject nonpublic endpoint", "", "actions/runs/7", "", nil, "", 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCIFixture(t, dir, "curl", []byte(fakePublicCurl), 0o700)
			writeCIFixture(t, dir, "sleep", []byte("#!/usr/bin/env bash\necho \"$1\" >>\"$STATE/sleeps\"\n"), 0o700)
			args := []string{"../../scripts/ci-public-gh.sh", "api"}
			if strings.HasSuffix(tc.endpoint, "/events") {
				args = append(args, "--paginate")
			}
			args = append(args, "repos/reliant-labs/forge/"+tc.endpoint)
			args = append(args, tc.args...)
			cmd := exec.Command("bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"MODE="+tc.mode, "STATE="+dir, "WANT_TOKEN="+tc.token, "GITHUB_TOKEN="+tc.token, "GH_TOKEN=ignored-by-adapter")
			got, err := cmd.CombinedOutput()
			exit := 0
			if ee, ok := err.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != tc.exit || !strings.Contains(string(got), tc.want) {
				t.Fatalf("want exit %d output %q, got exit %d\n%s", tc.exit, tc.want, exit, got)
			}
			sleeps, _ := os.ReadFile(filepath.Join(dir, "sleeps"))
			want := tc.sleeps
			if want == "SOON\n" {
				// Reset is 5s out; the adapter waits until one second past it.
				if s := strings.TrimSpace(string(sleeps)); s != "5" && s != "6" {
					t.Fatalf("want one wait of ~6s until X-RateLimit-Reset, got %q", sleeps)
				}
				return
			}
			if string(sleeps) != want {
				t.Fatalf("want waits %q, got %q", want, sleeps)
			}
		})
	}
}

// fakePublicCurl answers the adapter's curl call: -D headers, -o body, the
// status on stdout (-w), headers from stdin (-H @-). It fails the call when a
// token reaches argv, or when stdin's Authorization header is not exactly the
// one WANT_TOKEN names (none when it is empty). $STATE/calls counts attempts.
const fakePublicCurl = `#!/usr/bin/env bash
set -euo pipefail
[[ $1 == -q && $* != *'Authorization:'* && $* != *'ignored-by-adapter'* && $* == *'-H @-'* ]] || exit 99
auth=$(cat)
if [[ -n $WANT_TOKEN ]]; then [[ $auth == "Authorization: Bearer $WANT_TOKEN" ]] || exit 98
else [[ -z $auth ]] || exit 97; fi
[[ -z $WANT_TOKEN || $* != *"$WANT_TOKEN"* ]] || exit 96
while (($# > 1)); do
  case $1 in -D) headers=$2; shift ;; -o) body=$2; shift ;; esac
  shift
done
url=$1
n=$(( $(cat "$STATE/calls" 2>/dev/null || echo 0) + 1 )); echo "$n" >"$STATE/calls"
reply() { printf 'HTTP/2 %s\r\n%b\r\n' "$1" "${3:-}" >"$headers"; printf '%s' "$2" >"$body"; printf '%s' "$1"; }
pr='{"body":"proof","labels":[]}'
case $MODE in
  retry-after) ((n > 1)) || { reply 429 '{"message":"slow down"}' 'retry-after: 3\r\n'; exit 0; } ;;
  reset-soon) ((n > 1)) || { reply 403 '{"message":"API rate limit exceeded"}' "x-ratelimit-remaining: 0\r\nx-ratelimit-reset: $(( $(date +%s) + 5 ))\r\n"; exit 0; } ;;
  reset-late) reply 403 '{"message":"API rate limit exceeded"}' "x-ratelimit-remaining: 0\r\nx-ratelimit-reset: $(( $(date +%s) + 3000 ))\r\n"; exit 0 ;;
  secondary) ((n > 1)) || { reply 403 '{"message":"You have exceeded a secondary rate limit."}'; exit 0; } ;;
  502) reply 502 'bad gateway'; exit 0 ;;
  unreachable) echo 'curl: (7) Failed to connect to api.github.com port 443' >&2; printf 000; exit 7 ;;
  404) reply 404 '{"message":"Not Found"}'; exit 0 ;;
  forbidden) reply 403 '{"message":"Resource not accessible by integration"}'; exit 0 ;;
esac
case "$url" in
  https://api.github.com/repos/reliant-labs/forge/pulls/7\?*) reply 200 "$pr" ;;
  https://api.github.com/repos/reliant-labs/forge/issues/7/labels\?*)
    if [[ $MODE == malformed ]]; then reply 200 '{"not":"labels"}'; else reply 200 '[{"name":"breaking:approved"}]'; fi ;;
  https://api.github.com/repos/reliant-labs/forge/issues/7/events\?per_page=100\&page=1)
    reply 200 "$(jq -cn '[range(100) | {event:"labeled",actor:{login:"first"}}]')" ;;
  https://api.github.com/repos/reliant-labs/forge/issues/7/events\?per_page=100\&page=2)
    if [[ $MODE == rate-limit ]]; then reply 403 '{"message":"API rate limit exceeded"}' "x-ratelimit-remaining: 0\r\nx-ratelimit-reset: $(( $(date +%s) + 3000 ))\r\n"; exit 0; fi
    reply 200 '[{"event":"labeled","actor":{"login":"later"}}]' ;;
  *) exit 12 ;;
esac
`
