package secretredact

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestScrubAndUnprintable(t *testing.T) {
	r := New()
	value := "synthetic-secret-value"
	r.Register(value)
	for _, s := range []string{value, base64.StdEncoding.EncodeToString([]byte(value))} {
		if strings.Contains(r.Scrub(s), s) {
			t.Fatal("credential not scrubbed")
		}
	}
	for _, s := range []string{fmt.Sprint(r), fmt.Sprintf("%#v", r), fmt.Sprintf("%+v", r), r.LogValue().String()} {
		if strings.Contains(s, value) {
			t.Fatal("redactor prints credential")
		}
	}
	err := r.ScrubError(fmt.Errorf("remote: %w", errors.New(value)))
	if strings.Contains(err.Error(), value) || errors.Unwrap(err) != nil {
		t.Fatal("error chain leaked")
	}
}
