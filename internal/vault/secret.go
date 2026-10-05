// Copyright (c) 2025 Reliant Labs

package vault

import (
	"fmt"
	"io"
	"log/slog"
)

// Redacted is what every formatting path of a Secret produces.
const Redacted = "[redacted]"

// Secret holds plaintext that must never reach a log, an error string, JSON, or
// Temporal history. The bytes are unexported and every formatting interface
// answers Redacted, so the only way out is Use. That is a property of the
// type, not of caller discipline.
type Secret struct {
	b []byte
}

// NewSecret copies plaintext into a Secret. The caller still owns (and should
// zero) its own slice.
func NewSecret(plaintext []byte) Secret {
	return Secret{b: append([]byte(nil), plaintext...)}
}

// Use hands fn a private copy of the plaintext and zeroes it afterwards. fn
// must not retain the slice.
func (s Secret) Use(fn func([]byte) error) error {
	cp := append([]byte(nil), s.b...)
	defer clear(cp)
	return fn(cp)
}

// Len reports the plaintext length without exposing it.
func (s Secret) Len() int { return len(s.b) }

func (Secret) String() string   { return Redacted }
func (Secret) GoString() string { return Redacted }

// Format covers %v, %+v, %#v, %s, %q, %x and %d alike.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, Redacted) }

func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + Redacted + `"`), nil }
func (Secret) MarshalText() ([]byte, error) { return []byte(Redacted), nil }
func (Secret) LogValue() slog.Value         { return slog.StringValue(Redacted) }
