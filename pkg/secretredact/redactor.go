// Package secretredact scrubs credentials at transport boundaries without
// importing application schemas or protobuf registries.
package secretredact

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// Redacted is the value substituted for a registered credential.
const Redacted = "[redacted]"

// Redactor holds registered plaintext only for the lifetime of one call. It
// cannot be printed through fmt or slog. Register raw/escaped forms as needed.
type Redactor struct {
	mu      sync.Mutex
	secrets []string
}

func New() *Redactor                           { return &Redactor{} }
func (*Redactor) String() string               { return "secretredact.Redactor{}" }
func (r *Redactor) GoString() string           { return r.String() }
func (r *Redactor) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, r.String()) }
func (r *Redactor) LogValue() slog.Value       { return slog.StringValue(r.String()) }

// Register ignores values shorter than four bytes to avoid shredding text.
func (r *Redactor) Register(value string) {
	if len(value) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = append(r.secrets, value)
}

// Scrub removes raw and base64 forms from text before it leaves the boundary.
func (r *Redactor) Scrub(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.secrets {
		s = strings.ReplaceAll(s, v, Redacted)
		s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(v)), Redacted)
	}
	return s
}

// ScrubError drops the cause chain when it could disclose a credential.
func (r *Redactor) ScrubError(err error) error {
	if err == nil {
		return nil
	}
	if msg := r.Scrub(err.Error()); msg != err.Error() {
		return fmt.Errorf("%s", msg)
	}
	return err
}
