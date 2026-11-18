package dbgrants

import (
	"slices"
	"sort"
	"strings"
)

// # Who decides what the role may read
//
// The role reads a column only after SOMEONE DECIDED it may. Two declarations
// decide it, both made where the column lives:
//
//   - forge:secret   — the column is withheld. Declared on the proto field of
//     the entity message (it then also leaves the wire), or as
//     `COMMENT ON COLUMN t.c IS 'forge:secret'` in a migration, which is the
//     only place a column with no wire field can say it.
//   - forge:readable <reason> — the column is granted although its name looks
//     like a credential. Declared as `COMMENT ON COLUMN t.c IS 'forge:readable
//     <why this is not a credential>'`. The reason is mandatory: it is the
//     review record of an exception.
//
// A column that is credential-shaped (see [CredentialShaped]) and carries
// neither is UNDECIDED. It is withheld exactly like a secret one, so omission
// can never expose a credential, and `forge lint --db-grants` reports it as an
// error. Every other column is granted, which is what makes the role useful on
// the tables nobody thought to mark.

// ColumnKind is what a column's type says about whether it can hold a
// credential.
type ColumnKind uint8

const (
	// KindText is a column that can hold a credential (text, json, uuid, a
	// native enum, anything not named below): its NAME decides. It is the zero
	// value, so a caller that sets no kind gets the stricter reading.
	KindText ColumnKind = iota
	// KindScalar is a number, boolean or timestamp. `input_tokens bigint` and
	// `token_expires_at timestamptz` are not credentials whatever they are
	// called, so the name check does not apply.
	KindScalar
	// KindBinary is bytea: opaque by construction, and in practice ciphertext,
	// a hash or a key. It needs a decision whatever it is called.
	KindBinary
)

// Disposition is what the role is allowed to do with one column.
type Disposition uint8

const (
	// Granted: the role may SELECT the column.
	Granted Disposition = iota
	// WithheldSecret: declared forge:secret.
	WithheldSecret
	// WithheldUndecided: credential-shaped, and nobody decided.
	WithheldUndecided
)

// Disposition is the one place the three declarations are combined. Secret
// wins over Readable, because a column declared both ways is one somebody
// wanted hidden.
func (c Column) Disposition() Disposition {
	switch {
	case c.Secret:
		return WithheldSecret
	case c.Readable:
		return Granted
	case c.Kind == KindBinary, c.Kind == KindText && CredentialShaped(c.Name):
		return WithheldUndecided
	}
	return Granted
}

// CredentialNameStems are matched as substrings of the column name with its
// underscores removed, so `api_key`, `apikey` and `ApiKey` all match `apikey`.
// They are long enough to mean the thing wherever they sit in a name.
//
// `token` is a stem on purpose: it catches `refresh_token`, `token_hash`,
// `access_token`, `session_token`, and a false positive (`tokenizer`) costs one
// forge:readable declaration. A numeric `input_tokens` is exempt by type, not
// by name (see [KindScalar]).
var CredentialNameStems = []string{
	"password", "passwd", "passphrase",
	"secret", "credential",
	"token", "bearer",
	"ciphertext", "plaintext", "encrypted", "sealed",
	"apikey", "accesskey", "secretkey", "privatekey", "signingkey",
	"encryptionkey", "authkey", "masterkey", "licensekey", "sessionkey",
	"recoverycode", "backupcode", "resetcode", "verificationcode", "authcode",
}

// CredentialNameWords are matched as whole `_`-separated words only, because
// they are short enough to appear inside an innocent word (`pin` in `pinned`,
// `salt` in `basalt`). `hash` covers token and password hashes: a hash of a
// credential is not replayable but is an offline-guessing target, and a
// content hash that is not a credential takes a forge:readable.
var CredentialNameWords = []string{
	"hash", "hashed", "hashes", "salt",
	"jwt", "otp", "totp", "pin", "pwd",
	"cred", "creds", "cipher", "cookie",
}

// CredentialNameDescriptors are final words that make a name describe or point
// at a credential rather than hold one: `token_id`, `secret_name`,
// `credential_type`, `api_key_status`. A name ending in one is not
// credential-shaped. The list is short on purpose: `_prefix`, `_ref` and
// `_hint` are absent, because a "reference" or "prefix" column is often the
// value or most of it.
var CredentialNameDescriptors = []string{
	"id", "ids", "name", "names", "type", "kind", "status", "state",
	"count", "version", "provider", "scope", "scopes", "label",
}

// CredentialShaped reports whether a column NAME looks like it holds a
// credential: a token, a password, a key, ciphertext, a hash of one. It judges
// the name only; the type check is [ColumnKind].
//
// It is a deliberate over-approximation. Failing closed means a false positive
// costs one `forge:readable <reason>` and a false negative ships a credential,
// so the list errs toward matching. What it cannot catch is a credential in a
// column with an innocent name (`value`, `data`, `config`): mark those
// forge:secret.
func CredentialShaped(name string) bool {
	lower := strings.ToLower(name)
	words := strings.FieldsFunc(lower, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	if len(words) > 1 && slices.Contains(CredentialNameDescriptors, words[len(words)-1]) {
		return false
	}
	squashed := strings.Join(words, "")
	for _, stem := range CredentialNameStems {
		if strings.Contains(squashed, stem) {
			return true
		}
	}
	for _, word := range words {
		if slices.Contains(CredentialNameWords, word) {
			return true
		}
	}
	return false
}

// Finding is one undecided column.
type Finding struct {
	Schema string
	Table  string
	Column string
}

// String is the qualified name a report prints.
func (f Finding) String() string { return f.Schema + "." + f.Table + "." + f.Column }

// Undecided returns every credential-shaped column of tables that carries
// neither forge:secret nor forge:readable, sorted. They are the columns Render
// withholds without anyone having said so.
func Undecided(tables []Table) []Finding {
	var out []Finding
	for _, t := range tables {
		for _, c := range t.Columns {
			if c.Disposition() == WithheldUndecided {
				out = append(out, Finding{Schema: t.Schema, Table: t.Name, Column: c.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Schema != out[j].Schema {
			return out[i].Schema < out[j].Schema
		}
		if out[i].Table != out[j].Table {
			return out[i].Table < out[j].Table
		}
		return out[i].Column < out[j].Column
	})
	return out
}
