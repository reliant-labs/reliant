package cli

import "strings"

// linkForgeRerun is the command that re-runs the `project new` in progress
// bridged to a forge checkout: the user's own arguments plus --link-forge and
// --forge-root. root is the checkout when this binary knows it; otherwise the
// one thing forge cannot know is left as a placeholder, so everything else is
// copy-pasteable.
func linkForgeRerun(args []string, root string) string {
	if root == "" {
		root = "<path-to-forge-checkout>"
	}
	args = withoutFlags(args, "--link-forge", "--forge-root")
	// Name() is already a command line (`forge`, or `reliant forge` when
	// mounted in another binary), so only the arguments are quoted.
	words := []string{Name()}
	for _, a := range args {
		words = append(words, rerunWord(a))
	}
	return strings.Join(words, " ") + " --link-forge --forge-root " + rerunWord(root)
}

// withoutFlags drops the named flags, in `--flag`, `--flag=value` and
// `--flag value` form, so a re-run line never carries one twice. --link-forge
// is a bool: `--link-forge <x>` would make <x> a positional argument, so only
// --forge-root consumes a following value.
func withoutFlags(args []string, names ...string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		dropped := false
		for _, n := range names {
			switch {
			case strings.HasPrefix(a, n+"="):
				dropped = true
			case a == n:
				dropped = true
				if n == "--forge-root" && i+1 < len(args) {
					i++
				}
			}
		}
		if !dropped {
			out = append(out, a)
		}
	}
	return out
}

// rerunWord single-quotes s unless it holds only characters no POSIX shell
// interprets. A <placeholder> is left bare: it is for the reader to replace.
func rerunWord(s string) string {
	if s == "" {
		return "''"
	}
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return s
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:@,+%", r)) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}
