package release

import (
	"errors"
	"testing"
	"time"
)

// A declared shape needs the time it was declared — an undateable declaration
// cannot be judged stale — but the time may stand alone: the control plane's
// Live roster stamps that a declaration exists and serves the shape itself per
// environment, so a roster row carries the time without the shape.
func TestEnvRecordValidate_AShapeNeedsItsTimeButATimeStandsAlone(t *testing.T) {
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	shape := &Shape{Kind: EnvPersistent}

	if err := (EnvRecord{Name: "prod", Kind: EnvPersistent, DeclaredAt: &at}).Validate(); err != nil {
		t.Errorf("a roster row (time, no shape) was refused: %v", err)
	}
	if err := (EnvRecord{Name: "prod", Kind: EnvPersistent, DeclaredShape: shape, DeclaredAt: &at}).Validate(); err != nil {
		t.Errorf("a dated shape was refused: %v", err)
	}
	if err := (EnvRecord{Name: "prod", Kind: EnvPersistent, DeclaredShape: shape}).Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("an undated shape: got %v, want ErrInvalid", err)
	}
}
