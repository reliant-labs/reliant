package release

// THE SHARED SECRET RULE: which values in a rendered Kubernetes object are
// secret, and what a redacted value looks like.
//
// Three programs have to give the same answer, so there is one
// implementation, here, in the module all of them import:
//
//   - forge's bundle writer (internal/bundle), which refuses to seal a Secret
//     value into an artifact that is pushed, cached and kept forever (F-13);
//   - the shape hasher (RedactSecrets), which must never hash one;
//   - the control plane's config viewer, which shows a release's rendered
//     config to a person holding deploy:config:read, and must withhold
//     exactly what forge withholds plus what merely looks secret.
//
// The rule has two halves, and they are consumed differently on purpose.
//
// DECLARED (IsSecretObject, SecretObjectValues): every value under a Secret
// object's data or stringData is secret because the object says so, whatever
// it looks like. That is decidable, so the bundle writer REFUSES on it: a
// Secret value that is not the redaction marker (IsRedacted) fails the build.
//
// LOOKS SECRET (LooksSecret): a plain value (an env var's `value:`, a
// ConfigMap key) whose name or shape says it is a credential: a DSN with a
// password, a token pasted into `value:`. That is a heuristic, so nothing
// REFUSES on it. A plain value is configuration the author declared in the
// open, a build that refused it would refuse a legitimate deploy on a guess,
// and the bundle is not where it leaks from: the manifests layer is readable
// only by the deploy path. Where it would leak from is a screen, so the
// viewer withholds it (RedactObject) without exposing a value-derived digest.
//
// One file, so a change to either half is a change to both consumers: the
// build and the viewer cannot drift into disagreeing about what a Secret's
// values are, or about what the marker is.

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/redact"
)

// RedactedWithheld is what RedactObject shows for every value of a Secret
// object: the key survives, the value (even its digest) does not. It carries
// RedactedSecretPrefix, so IsRedacted recognizes it and a second pass leaves
// it alone.
const RedactedWithheld = RedactedSecretPrefix + "withheld"

// redactedDigestHexLen is retained for the explicit RedactedDigest utility.
// RedactObject never uses this value-derived fingerprint for viewer output.
const redactedDigestHexLen = 12

// IsSecretObject reports whether a decoded rendered document is a Secret: the
// one kind whose data and stringData hold values that are secret by
// declaration. It matches on kind alone, as every consumer always has, so an
// object of another group that calls itself a Secret is treated as one (the
// safe direction).
func IsSecretObject(doc any) bool {
	body, ok := doc.(map[string]any)
	return ok && body["kind"] == "Secret"
}

// SecretObjectValue is one value of a Secret object: the field it sits under
// (data or stringData), its key, and the value as decoded.
type SecretObjectValue struct {
	Field string
	Key   string
	Value any
}

// SecretObjectValues lists every value a Secret object holds, data before
// stringData and keys sorted, so a refusal names the same key on every run.
// A document that is not a Secret, or a field that is not a map, holds none.
func SecretObjectValues(doc any) []SecretObjectValue {
	if !IsSecretObject(doc) {
		return nil
	}
	body := doc.(map[string]any)
	var out []SecretObjectValue
	for _, field := range secretValueFields {
		values, ok := body[field].(map[string]any)
		if !ok {
			continue
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = append(out, SecretObjectValue{Field: field, Key: key, Value: values[key]})
		}
	}
	return out
}

// IsRedacted reports whether a value is already a redaction: a string that
// begins with RedactedSecretPrefix. Every redaction this package writes
// (RedactSecrets' full digest, RedactedDigest's short one, RedactedWithheld)
// does, and a non-string never is.
func IsRedacted(value any) bool {
	s, ok := value.(string)
	return ok && strings.HasPrefix(s, RedactedSecretPrefix)
}

// RedactedDigest returns an unsalted, truncated SHA-256 fingerprint.
// It is not safe redaction for secret values: it permits offline dictionary
// tests, even behind a config-read grant. RedactObject never uses it.
// Secret comparisons require a separately keyed digest supplied by the backend;
// without that key, viewers must omit secret value diffs. Idempotent.
func RedactedDigest(value string) string {
	if IsRedacted(value) {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return RedactedSecretPrefix + "sha256:" + hex.EncodeToString(sum[:])[:redactedDigestHexLen]
}

// credentialNameWords are the words that make a NAME a credential's: an env
// var or config key containing any of them, in any case, is withheld whatever
// its value. A substring match, so it over-withholds (TOKENIZER, KEYCLOAK_URL)
// rather than miss DB_PASSWORD_PROD; over-withholding costs a visible value,
// under-withholding a leak. AUTH includes Authorization, Proxy-Authorization
// and X-Auth headers; COOKIE and SESSION protect low-entropy session values.
var credentialNameWords = []string{"SECRET", "TOKEN", "PASSWORD", "PASSWD", "KEY", "DSN", "CREDENTIAL", "AUTH", "COOKIE", "SESSION"}

func secretName(name string) bool {
	upper := strings.ToUpper(name)
	for _, word := range credentialNameWords {
		if strings.Contains(upper, word) {
			return true
		}
	}
	return false
}

// contentDigestRE matches a content digest (`sha256:<64 hex>`, as in an image
// reference `repo@sha256:…`). It names content, not a credential, and
// without the exemption every pinned image would read as an opaque run.
var contentDigestRE = regexp.MustCompile(`\bsha(?:256|512):[0-9a-f]{64,128}\b`)

// tokenAlphabetRE is the alphabet a bare token is written in (base64,
// base64url, hex). A value with a space, dot, colon or @ is a sentence,
// hostname or URL, which the shape rules judge instead.
var tokenAlphabetRE = regexp.MustCompile(`^[A-Za-z0-9+/=_-]+$`)

// flagRE is a command-line flag's name: `-v`, `--db-password`. Anything else
// that starts with a dash (a PEM block) is judged by shape, never split.
var flagRE = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var hexRE = regexp.MustCompile(`^[0-9a-fA-F]+$`)

// gitCommitRE is a full git commit id: it names a build, not a secret, and
// forge stamps one into env vars a release carries.
var gitCommitRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// LooksSecret reports whether a PLAIN value, under name, should be withheld
// from a person reading config: the "looks secret" half of the rule.
//
//   - by NAME: name contains one of SECRET, TOKEN, PASSWORD, PASSWD, KEY,
//     DSN, CREDENTIAL, AUTH, COOKIE or SESSION (any case);
//   - by SHAPE: value holds a credential's written form, judged by the
//     patterns pkg/redact removes from text that leaves a process (URL
//     userinfo, a PEM private key, a JWT, a bearer or basic credential, a
//     credential-named key=value pair, a provider key prefix, a long opaque
//     run), so the viewer and forge's bug reports hold one bar;
//   - by ENTROPY: value is a bare token of 20+ characters mixing upper case,
//     lower case and digits (or 32+ hex) with high per-character entropy.
//
// An empty value has nothing to withhold, and a value that is already a
// redaction (IsRedacted) is not withheld again. name may be empty when a
// value has none (a container arg), which leaves shape and entropy.
func LooksSecret(name, value string) bool {
	if value == "" || IsRedacted(value) {
		return false
	}
	return secretName(name) || hasCredentialShape(value) || isHighEntropyToken(value)
}

// hasCredentialShape is the SHAPE test alone, for a string with no name of
// its own (an annotation, a field of a custom resource). Content digests are
// exempted first.
func hasCredentialShape(value string) bool {
	if IsRedacted(value) {
		return false
	}
	v := contentDigestRE.ReplaceAllString(value, "sha256:")
	return redact.Secrets(v) != v
}

// isHighEntropyToken reports a bare token that looks generated rather than
// written: the token alphabet only, at least 20 characters, and either 32+
// hex characters that are not a git commit id, or upper+lower+digit mixed the
// way a generator mixes them (≥3.5 bits of entropy per character, and the
// character class changing at least every third character, which a CamelCase
// name with a year on the end does not). Kebab-case names, UUIDs, hostnames
// and paths fail the alphabet or the mix and are shown.
func isHighEntropyToken(value string) bool {
	if len(value) < 20 || !tokenAlphabetRE.MatchString(value) || gitCommitRE.MatchString(value) {
		return false
	}
	if len(value) >= 32 && hexRE.MatchString(value) {
		return true
	}
	var hasUpper, hasLower, hasDigit bool
	transitions, prev := 0, -1
	for _, r := range value {
		class := 3
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper, class = true, 0
		case r >= 'a' && r <= 'z':
			hasLower, class = true, 1
		case r >= '0' && r <= '9':
			hasDigit, class = true, 2
		}
		if prev >= 0 && class != prev {
			transitions++
		}
		prev = class
	}
	return hasUpper && hasLower && hasDigit &&
		transitions*3 >= len(value) && shannonEntropy(value) >= 3.5
}

// shannonEntropy is the per-character Shannon entropy of s, in bits.
func shannonEntropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// RedactObject returns a copy of a decoded rendered object (a map, as a YAML
// or JSON decoder produces) with every secret value withheld: what the config
// viewer may show a person holding deploy:config:read. The input is never
// mutated, and redacting twice equals redacting once.
//
//   - A Secret keeps its key names only: every data and stringData value
//     becomes RedactedWithheld, even one already redacted, because a value's
//     digest is still about the value.
//   - A secret REFERENCE (valueFrom.secretKeyRef, envFrom.secretRef, a
//     volume's secretName, an ExternalSecret) is a name, not a value, and
//     stays as it is.
//   - A named plain value (any {name, value} pair: a container's env, a
//     probe's httpHeaders, a forge Workload's env) becomes RedactedWithheld
//     when LooksSecret(name, value). So do a ConfigMap's data values, by key.
//   - A ConfigMap's binaryData is always withheld: it is not text a person
//     reads, and a keystore is binary.
//   - A container's args and command are judged per element: `--flag=value`
//     and `--flag value` by the flag's name and the value, anything else by
//     shape.
//   - Every other string is withheld when it has a credential's shape.
//   - Sensitive names carry their policy through malformed values: a nested
//     map/list, number or nil is withheld as a whole, not recursively exposed.
//     Malformed names and non-sequence args/command fields fail closed too.
//
// No value-derived fingerprint is exposed. Secret changes simply read as
// redacted; comparisons require a separately keyed backend digest in the future.
// The rendered YAML a viewer shows must be produced from this output, never
// from the raw layer bytes.
func RedactObject(doc any) any {
	return redactTree(doc)
}

// redactObjectFields applies declaration-specific policy to one already-copied
// object. Every map visits this function, including objects nested inside Lists
// or extension fields; it never recurses back into the same object.
func redactObjectFields(body, out map[string]any) map[string]any {
	switch {
	case IsSecretObject(body):
		for _, field := range secretValueFields {
			raw, present := body[field]
			if !present {
				continue
			}
			values, ok := stringMap(raw)
			if !ok {
				out[field] = RedactedWithheld
				continue
			}
			withheld := make(map[string]any, len(values))
			for key := range values {
				withheld[key] = RedactedWithheld
			}
			out[field] = withheld
		}
	case body["kind"] == "ConfigMap":
		if values, ok := stringMap(body["data"]); ok {
			data := make(map[string]any, len(values))
			for key, value := range values {
				data[key] = redactNamed(key, value)
			}
			out["data"] = data
		} else if _, present := body["data"]; present {
			out["data"] = RedactedWithheld
		}
		if values, ok := stringMap(body["binaryData"]); ok {
			binary := make(map[string]any, len(values))
			for key := range values {
				binary[key] = RedactedWithheld
			}
			out["binaryData"] = binary
		} else if _, present := body["binaryData"]; present {
			out["binaryData"] = RedactedWithheld
		}
	}
	return out
}

// stringMap accepts the string-keyed map forms callers and YAML/JSON decoders
// produce. Conversion is a copy, never a mutation of the input.
func stringMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[string]string:
		out := make(map[string]any, len(m))
		for key, value := range m {
			out[key] = value
		}
		return out, true
	default:
		return nil, false
	}
}

// redactTree copies v, withholding by the named-value, args and shape rules.
func redactTree(v any) any {
	switch t := v.(type) {
	case map[string]any:
		rawName, hasName := t["name"]
		name, named := rawName.(string)
		out := make(map[string]any, len(t))
		for key, child := range t {
			switch {
			case hasName && key == "value":
				if !named {
					out[key] = RedactedWithheld
				} else {
					out[key] = redactNamed(name, child)
				}
			case key == "value" && !hasName:
				out[key] = RedactedWithheld
			case key == "env" || key == "httpHeaders":
				out[key] = redactNamedValues(child)
			case key == "args" || key == "command":
				out[key] = redactArgs(child)
			default:
				out[key] = redactTree(child)
			}
		}
		return redactObjectFields(t, out)
	case map[string]string:
		m := make(map[string]any, len(t))
		for key, value := range t {
			m[key] = value
		}
		return redactTree(m)
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = redactTree(child)
		}
		return out
	case []string:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = redactTree(child)
		}
		return out
	case string:
		if IsRedacted(t) || hasCredentialShape(t) {
			return RedactedWithheld
		}
		return t
	default:
		return v
	}
}

// redactNamedValues handles env/header arrays without trusting malformed ASTs.
func redactNamedValues(v any) any {
	items, ok := v.([]any)
	if !ok {
		return RedactedWithheld
	}
	out := make([]any, len(items))
	for i, item := range items {
		entry, ok := stringMap(item)
		if !ok {
			out[i] = RedactedWithheld
			continue
		}
		if _, named := entry["name"].(string); !named {
			out[i] = RedactedWithheld
			continue
		}
		out[i] = redactTree(entry)
	}
	return out
}

// redactNamed withholds the entire value of a sensitive name, regardless of
// its AST type. Recursing would lose the name's secret context.
func redactNamed(name string, value any) any {
	if secretName(name) || IsRedacted(value) {
		return RedactedWithheld
	}
	s, ok := value.(string)
	if !ok {
		return redactTree(value)
	}
	if LooksSecret(name, s) {
		return RedactedWithheld
	}
	return s
}

// redactArgs judges a container's args or command element by element. A
// `--flag=value` element is judged by the flag's name; a bare `--flag`
// followed by a separate element lends that element its name.
func redactArgs(v any) any {
	var items []any
	switch t := v.(type) {
	case []any:
		items = t
	case []string:
		for _, s := range t {
			items = append(items, s)
		}
	default:
		// args/command must be a sequence. An arbitrary malformed tree has
		// no reliable flag/value boundary, so do not expose any of it.
		return RedactedWithheld
	}
	out := make([]any, len(items))
	flag := ""
	for i, item := range items {
		if secretName(flag) {
			// Consume the value before parsing it, including nil, a nested AST
			// or a flag-looking string. Malformation cannot escape the context.
			out[i] = RedactedWithheld
			flag = ""
			continue
		}
		s, ok := item.(string)
		if !ok {
			out[i] = redactTree(item)
			flag = ""
			continue
		}
		name, value, hasValue := strings.Cut(s, "=")
		switch {
		case hasValue && flagRE.MatchString(name):
			if secretName(strings.TrimLeft(name, "-")) || IsRedacted(value) || LooksSecret(strings.TrimLeft(name, "-"), value) {
				out[i] = name + "=" + RedactedWithheld
			} else {
				out[i] = s
			}
			flag = ""
		case flagRE.MatchString(s):
			out[i] = s
			flag = strings.TrimLeft(s, "-")
		default:
			if IsRedacted(s) || LooksSecret(flag, s) {
				out[i] = RedactedWithheld
			} else {
				out[i] = s
			}
			flag = ""
		}
	}
	return out
}
