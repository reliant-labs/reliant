package tdd_test

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/tdd"
)

// fakeMock mimics the shape Forge generates: a struct of Func fields
// where each field corresponds to one contract method.
type fakeMock struct {
	GetFunc func(id string) (string, error)
	PutFunc func(id, value string) error
}

func TestNewMock_AppliesOptions(t *testing.T) {
	m := tdd.NewMock(
		func(m *fakeMock) {
			m.GetFunc = func(id string) (string, error) { return "fixed-" + id, nil }
		},
		func(m *fakeMock) {
			m.PutFunc = func(_, _ string) error { return nil }
		},
	)
	if m.GetFunc == nil || m.PutFunc == nil {
		t.Fatalf("options did not set both Func fields: %+v", m)
	}
	got, err := m.GetFunc("x")
	if err != nil || got != "fixed-x" {
		t.Fatalf("GetFunc returned (%q, %v)", got, err)
	}
}

func TestNewMock_ZeroValueOnNoOptions(t *testing.T) {
	m := tdd.NewMock[fakeMock]()
	if m.GetFunc != nil || m.PutFunc != nil {
		t.Fatalf("expected zero-valued mock, got %+v", m)
	}
}
