package cli

import (
	"net/url"
	"strings"

	"github.com/reliant-labs/forge/pkg/redact"
	"github.com/reliant-labs/forge/pkg/release"
)

// dockerCommandLine renders a `docker` argv as the one shell command it is:
// every argument, in order, shell-quoted only where a shell would need it, so
// the line forge prints for an image build is the line a person can read and
// re-run. A summary ("docker build -f Dockerfile . (2 tags)") hid the build
// args, the --platform and the tags — the arguments that decide what the
// image is — so a wrong one could not be seen in the log (bug 361c0e72).
//
// Credentials are withheld, never printed. A `--build-arg` value whose name or
// shape looks secret (release.LooksSecret, the rule forge's release viewer
// holds) prints as redact.Marker with its name kept, and a URL credential in a
// `--build-context` value keeps the URL and loses the password. Build args are
// not a private channel — docker records them in the image history — but the
// build log is the wrong place to find out a value should not have been one.
func dockerCommandLine(args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "docker")
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--build-arg" || a == "--build-context") && i+1 < len(args):
			parts = append(parts, a, shellWord(withholdFlagValue(a, args[i+1])))
			i++
		case strings.HasPrefix(a, "--build-arg=") || strings.HasPrefix(a, "--build-context="):
			flag, value, _ := strings.Cut(a, "=")
			parts = append(parts, shellWord(flag+"="+withholdFlagValue(flag, value)))
		default:
			parts = append(parts, shellWord(a))
		}
	}
	return strings.Join(parts, " ")
}

// withholdFlagValue is a `name=value` argument of flag with any credential in
// value withheld.
func withholdFlagValue(flag, arg string) string {
	name, value, ok := strings.Cut(arg, "=")
	if !ok {
		// `--build-arg NAME` passes the caller's environment variable through:
		// there is no value on the command line to withhold.
		return arg
	}
	if flag == "--build-arg" {
		if release.LooksSecret(name, value) {
			return name + "=" + redact.Marker
		}
		return arg
	}
	// --build-context: a path, a docker-image:// ref or a git URL. Only a
	// URL can carry a credential, and the rest of it is worth reading.
	if strings.Contains(value, "://") {
		if u, err := url.Parse(value); err == nil && u.User != nil {
			// A lone userinfo field (https://<token>@github.com) is the
			// token itself, so it goes whole; a user:password pair keeps
			// the user.
			userinfo := redact.Marker
			if _, hasPassword := u.User.Password(); hasPassword {
				userinfo = u.User.Username() + ":" + redact.Marker
			}
			u.User = nil
			scheme, rest, _ := strings.Cut(u.String(), "://")
			return name + "=" + scheme + "://" + userinfo + "@" + rest
		}
	}
	return arg
}

// shellWord is s as one POSIX shell word: unchanged when every byte is one a
// shell never interprets, otherwise single-quoted.
func shellWord(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !shellSafeRune(r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func shellSafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("-_./:=@%+,", r)
}
