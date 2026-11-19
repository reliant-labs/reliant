// File: internal/cli/lint/lint_identity_methods.go
//
// forgeconv-identity-methods — a forge.ManagedIdentity that declares a sign-in
// method or sign-up mode the project's login broker does not serve.
//
// internal/app/login_broker.go is the project's own file, so a broker
// scaffolded before identity v2 never learns the new methods: it reads no
// idp_login_policy and mounts no passkey, magic-link or social route. Declaring
// `methods = ["passkey"]`, `social = [...]` or `signup = "open"` against it
// converges the identity instance and changes nothing on the sign-in screen —
// the declaration is silently ignored. This lane names each such gap.
//
// The check is textual on both sides, and deliberately so: the declaration is
// read from deploy/kcl/**/*.k (comments stripped), the broker from the Go
// files under internal/ (a mount of the route constant, or of a literal
// naming the route). A false positive costs a reworded mount; a missed
// declaration costs nothing worse than today.
//
// ERROR severity, graded by the rollout table: a warning until GatesFrom.
package lint

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ruleIdentityMethods is the rule ID this lane emits.
const ruleIdentityMethods = "forgeconv-identity-methods"

// identityDemand is one thing a declaration asks the broker to serve.
type identityDemand struct {
	what string // what the KCL declares, for the message
	// mounts are the route constants or literal paths that serve it; one is
	// enough.
	mounts []string
	where  string // the KCL file:line that declared it
}

var (
	kclLineComment = regexp.MustCompile(`#.*$`)
	kclMethods     = regexp.MustCompile(`\bmethods\s*[:=]\s*\[([^\]]*)\]`)
	kclSocial      = regexp.MustCompile(`\bsocial\s*[:=]\s*\[\s*forge\.Identity(Google|GitHub)\b`)
	kclSignup      = regexp.MustCompile(`\bsignup\s*[:=]\s*"(open|domain)"`)
)

// identityDemands reads what the project's KCL declares.
func identityDemands(root string) ([]identityDemand, error) {
	var out []identityDemand
	dir := filepath.Join(root, "deploy", "kcl")
	seen := map[string]bool{}
	add := func(d identityDemand) {
		if !seen[d.what] {
			seen[d.what] = true
			out = append(out, d)
		}
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".k") || strings.HasSuffix(path, "_test.k") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// Matched on the whole comment-stripped file (a methods list may
		// span lines), each match mapped back to the line it starts on.
		lines := strings.Split(string(raw), "\n")
		for i := range lines {
			lines[i] = kclLineComment.ReplaceAllString(lines[i], "")
		}
		text := strings.Join(lines, "\n")
		lineOf := func(off int) int { return strings.Count(text[:off], "\n") + 1 }
		for _, m := range kclMethods.FindAllStringSubmatchIndex(text, -1) {
			list := text[m[2]:m[3]]
			at := fmt.Sprintf("%s:%d", rel, lineOf(m[0]))
			if strings.Contains(list, `"passkey"`) {
				add(identityDemand{what: `methods "passkey"`, mounts: []string{"PasskeyBeginPath", "/auth/passkey/begin"}, where: at})
			}
			if strings.Contains(list, `"magic_link"`) {
				add(identityDemand{what: `methods "magic_link"`, mounts: []string{"MagicRequestPath", "/auth/magic/request"}, where: at})
			}
		}
		for _, m := range kclSocial.FindAllStringIndex(text, -1) {
			add(identityDemand{what: "social sign-in", mounts: []string{"SocialStartPath", "/auth/social/start"},
				where: fmt.Sprintf("%s:%d", rel, lineOf(m[0]))})
		}
		for _, m := range kclSignup.FindAllStringSubmatchIndex(text, -1) {
			add(identityDemand{what: fmt.Sprintf("signup %q", text[m[2]:m[3]]), mounts: []string{"RegisterPath", "/auth/register"},
				where: fmt.Sprintf("%s:%d", rel, lineOf(m[0]))})
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return out, nil
}

// brokerSource is every non-test Go file under internal/ that mounts auth
// routes, concatenated with its path, so a mount in any of them counts.
type brokerSource struct {
	text string
	path string // the file that mounts LoginPath, for the finding
}

func readBrokerSource(root string) (brokerSource, error) {
	var b strings.Builder
	var at string
	dir := filepath.Join(root, "internal")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != dir && (dotenvSkipDirs[d.Name()] || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil || !strings.Contains(string(src), "/auth/") {
			return nil
		}
		if at == "" && strings.Contains(string(src), "RegisterLoginBroker") {
			rel, _ := filepath.Rel(root, path)
			at = filepath.ToSlash(rel)
		}
		b.Write(src)
		b.WriteByte('\n')
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return brokerSource{}, err
	}
	return brokerSource{text: b.String(), path: at}, nil
}

// mounts reports whether the source mounts one of the routes.
func (s brokerSource) mounts(routes []string) bool {
	for _, r := range routes {
		if strings.HasPrefix(r, "/") {
			if regexp.MustCompile(`\.Handle(Func)?\(\s*"(\w+ )?` + regexp.QuoteMeta(r) + `"`).MatchString(s.text) {
				return true
			}
			continue
		}
		if regexp.MustCompile(`\.Handle(Func)?\(\s*` + regexp.QuoteMeta(r) + `\s*,`).MatchString(s.text) {
			return true
		}
	}
	return false
}

// readsLoginPolicy reports whether the broker reads the delivered document.
func (s brokerSource) readsLoginPolicy() bool {
	return strings.Contains(s.text, "ParseLoginPolicy(") || strings.Contains(s.text, "IdpLoginPolicy")
}

type identityMethodsFinding struct {
	Where   string // the KCL declaration
	Broker  string // the broker file, "" when there is none
	What    string
	Missing string
}

func findIdentityMethodGaps(root string) ([]identityMethodsFinding, error) {
	demands, err := identityDemands(root)
	if err != nil || len(demands) == 0 {
		return nil, err
	}
	src, err := readBrokerSource(root)
	if err != nil {
		return nil, err
	}
	if src.path == "" {
		// No native broker: the app uses the issuer-hosted pages, or its
		// own sign-in entirely. Nothing to compare against.
		return nil, nil
	}
	var out []identityMethodsFinding
	policy := src.readsLoginPolicy()
	for _, d := range demands {
		switch {
		case !policy:
			out = append(out, identityMethodsFinding{Where: d.where, Broker: src.path, What: d.what,
				Missing: "the broker reads no idp_login_policy (devidp.ParseLoginPolicy), so it never learns the declaration"})
		case !src.mounts(d.mounts):
			out = append(out, identityMethodsFinding{Where: d.where, Broker: src.path, What: d.what,
				Missing: "the broker mounts no " + d.mounts[0]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Where+out[i].What < out[j].Where+out[j].What })
	return out, nil
}

func collectIdentityMethodsJSON(root string) ([]lintJSONFinding, error) {
	if root == "" {
		return nil, nil
	}
	found, err := findIdentityMethodGaps(root)
	if err != nil {
		return nil, fmt.Errorf("scan identity sign-in methods: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(found))
	for _, f := range found {
		file, line := f.Where, 0
		if i := strings.LastIndexByte(f.Where, ':'); i > 0 {
			file = f.Where[:i]
			_, _ = fmt.Sscanf(f.Where[i+1:], "%d", &line)
		}
		out = append(out, lintJSONFinding{
			Rule:     ruleIdentityMethods,
			Severity: "error",
			File:     file,
			Line:     line,
			Message: "the identity declares " + f.What + " but " + f.Broker + " does not serve it: " + f.Missing +
				"; the declaration is converged and silently ignored by the sign-in screen",
			FixHint: "apply the identity-v2 broker edits (`forge skill load migrations/v1.1.0/identity-v2` has them exactly), " +
				"or drop the declaration",
		})
	}
	return out, nil
}
