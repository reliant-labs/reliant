package redact

import (
	"net/url"
	"strings"
)

// dsnPassword replaces a secret in a connection string. It is plain text
// rather than Marker so a redacted URL stays readable: brackets would be
// percent-encoded in the userinfo.
const dsnPassword = "xxxxx"

// DSN masks every secret a postgres connection string carries, for an error
// message: the URL's userinfo password, a password or sslpassword query
// parameter, and a keyword/value string's password= or sslpassword=, in any
// casing and every time one repeats. A URL's fragment is dropped. passfile is
// a path, not a secret, and is kept.
//
// Unlike Secrets, it reads the string the way the driver does rather than
// matching text: a URL's query keys are percent-decoded first (pgx reads
// ?%70assword= as password), and a keyword/value string is tokenized as libpq
// and pgx tokenize it (single-quoted values, backslash escapes, space around
// =). A string that does not parse as either is returned as Marker, whole:
// one whose shape is unknown is one whose secrets cannot be found.
func DSN(dsn string) string {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		if out, ok := keywordValueDSN(dsn); ok {
			return out
		}
		return Marker
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return Marker
	}
	if u.User != nil {
		if _, ok := u.User.Password(); ok {
			u.User = url.UserPassword(u.User.Username(), dsnPassword)
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Marker
	}
	for k := range q {
		if isDSNSecretKey(k) {
			q[k] = []string{dsnPassword}
		}
	}
	u.RawQuery = q.Encode()
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// isDSNSecretKey reports whether a connection-string keyword names a secret:
// the driver's password and sslpassword, and, since masking too much costs
// nothing, any keyword that mentions a password in any casing.
func isDSNSecretKey(k string) bool {
	return strings.Contains(strings.ToLower(k), "password")
}

// keywordValueDSN masks the secret values of a keyword/value connection
// string, tokenized as pgx's parseKeywordValueSettings does: a keyword is a
// run of non-space characters, then optional space, '=', optional space, then
// a value that is either single-quoted (backslash escapes, closing quote
// required) or runs to the next unescaped space. Everything else is kept as
// written. ok is false for a string the driver would not parse.
func keywordValueDSN(s string) (string, bool) {
	const space = " \t\n\r\v\f"
	if strings.IndexByte(s, 0) >= 0 {
		return "", false
	}
	isSpace := func(c byte) bool { return strings.IndexByte(space, c) >= 0 }
	var out strings.Builder
	i := 0
	for {
		for i < len(s) && isSpace(s[i]) {
			out.WriteByte(s[i])
			i++
		}
		if i == len(s) {
			return out.String(), true
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return "", false
		}
		key := strings.TrimRight(s[i:i+eq], space)
		if key == "" || strings.ContainsAny(key, space) {
			return "", false
		}
		out.WriteString(s[i : i+eq+1])
		i += eq + 1
		for i < len(s) && isSpace(s[i]) {
			out.WriteByte(s[i])
			i++
		}
		start := i
		if i < len(s) && s[i] == '\'' {
			i++
			for ; i < len(s) && s[i] != '\''; i++ {
				if s[i] == '\\' {
					i++
				}
			}
			if i >= len(s) {
				return "", false // an unterminated quoted value
			}
			i++ // the closing quote
		} else {
			for ; i < len(s) && !isSpace(s[i]); i++ {
				if s[i] == '\\' {
					i++
				}
			}
			if i > len(s) {
				i = len(s) // a trailing backslash
			}
		}
		if isDSNSecretKey(key) {
			out.WriteString(dsnPassword)
		} else {
			out.WriteString(s[start:i])
		}
	}
}
