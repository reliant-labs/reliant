package forgeconv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

// forgeOwnedFrontendEnvVar matches a dotenv assignment of a frontend
// variable forge OWNS — declares in the KCL `config` / `env_vars` block and
// injects at both dev launch and build time — across the three framework
// prefixes (Next.js NEXT_PUBLIC_, Vite VITE_, Expo EXPO_PUBLIC_):
//
//	*_MOCK_API        mock mode (the load-bearing one — a committed
//	                  *_MOCK_API=true is the classic "mock shipped to prod")
//	*_API_URL         API base URL (forge bakes the dev floor + per-env URL)
//	*_OTEL_ENDPOINT   browser OTLP/HTTP trace endpoint
//	*_ENVIRONMENT     logical environment label
//
// A committed value fights the KCL-injected one (build path: forge wins via
// MergeExtraWins, so the dotenv is dead weight that misleads; dev path: the
// dotenv can shadow the intended default). Either way it belongs in KCL.
var forgeOwnedFrontendEnvVar = regexp.MustCompile(
	`^\s*(?:export\s+)?((?:NEXT_PUBLIC_|VITE_|EXPO_PUBLIC_)(?:MOCK_API|API_URL|OTEL_ENDPOINT|ENVIRONMENT))\s*=`)

// isFrontendEnvFile reports whether base is a dotenv file this rule scans:
// `.env`, `.env.local`, `.env.<name>`, `.env.<name>.local`. Documentation
// / template variants (.env.example, .env.local.sample, *.tmpl, the
// vite-env.d.ts typings) are NOT active build inputs and are skipped.
func isFrontendEnvFile(base string) bool {
	if base == ".env" || base == ".env.local" {
		return true
	}
	if !strings.HasPrefix(base, ".env.") {
		return false
	}
	switch {
	case strings.HasSuffix(base, ".example"),
		strings.HasSuffix(base, ".sample"),
		strings.HasSuffix(base, ".tmpl"),
		strings.HasSuffix(base, ".d.ts"):
		return false
	}
	return true
}

// LintFrontendEnvFiles scans each frontend source directory for committed
// dotenv files (.env / .env.local / .env.<env>) that hard-code a
// forge-owned frontend variable, and reports each as a finding at the
// given severity.
//
// The severity is the caller's — this is the SINGLE analyzer behind the
// dev/build severity split: `forge lint` calls it at SeverityWarning (a
// nudge, never gates), while the build/deploy path calls it at
// SeverityError (a hard-coded forge-owned var — mock above all — must not
// reach a shipped build). Routing both through one analyzer keeps the two
// verdicts from ever diverging.
//
// feDirs are frontend source dirs (absolute, or resolvable by the caller);
// root is used only to relativize the reported File path for stable
// output. A missing / unreadable frontend dir is silently skipped — this
// rule flags committed files, not absent ones.
func LintFrontendEnvFiles(root string, feDirs []string, sev finding.Severity) Result {
	var res Result
	seen := map[string]bool{}
	for _, dir := range feDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !isFrontendEnvFile(e.Name()) {
				continue
			}
			full := filepath.Join(dir, e.Name())
			if seen[full] {
				continue
			}
			seen[full] = true
			res.Findings = append(res.Findings, scanFrontendEnvFile(root, full, sev)...)
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		if res.Findings[i].File != res.Findings[j].File {
			return res.Findings[i].File < res.Findings[j].File
		}
		return res.Findings[i].Line < res.Findings[j].Line
	})
	return res
}

// scanFrontendEnvFile reads one dotenv file and emits a finding for every
// non-comment line that assigns a forge-owned frontend variable.
func scanFrontendEnvFile(root, path string, sev finding.Severity) []Finding {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	reported := path
	if rel, relErr := filepath.Rel(root, path); relErr == nil && !strings.HasPrefix(rel, "..") {
		reported = rel
	}

	var findings []Finding
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 256*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := forgeOwnedFrontendEnvVar.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		findings = append(findings, Finding{
			Rule:     "forgeconv-frontend-env-forge-owned",
			Severity: sev,
			File:     reported,
			Line:     line,
			Message: fmt.Sprintf(
				"%s sets forge-owned frontend variable %q; forge injects this from KCL at build + dev launch, so a committed dotenv value drifts (and for *_MOCK_API risks shipping a mock build)",
				filepath.Base(path), m[1]),
			Remediation: "declare it in the frontend's KCL `config` block (mock / api_url / otel_endpoint / environment) or `env_vars`, then delete the dotenv line — see the Frontend schema's FrontendConfig",
		})
	}
	return findings
}
