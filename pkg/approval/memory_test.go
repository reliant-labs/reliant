package approval

import (
	"testing"
	"time"
)

func TestMemoryStore_Contract(t *testing.T) {
	runContract(t, func(t *testing.T, clock *fakeClock) Approvals {
		return NewMemoryStore(WithNow(clock.Now))
	})
}

func TestDeadlineHelpers(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	closeAt := now.Add(5 * time.Hour)
	if got := At(closeAt); !got.Equal(closeAt) {
		t.Errorf("At = %v", got)
	}
	if got := Within(now, time.Hour); !got.Equal(now.Add(time.Hour)) {
		t.Errorf("Within = %v", got)
	}
	if got := NextOr(closeAt, now, 8*time.Hour); !got.Equal(closeAt) {
		t.Errorf("NextOr(next) = %v", got)
	}
	if got := NextOr(time.Time{}, now, 8*time.Hour); !got.Equal(now.Add(8 * time.Hour)) {
		t.Errorf("NextOr(zero) = %v", got)
	}
}

func TestWithSchema_TableName(t *testing.T) {
	for _, tc := range []struct {
		opts []StoreOption
		want string
	}{
		{nil, `approvals`},
		{[]StoreOption{WithSchema("")}, `approvals`},
		{[]StoreOption{WithSchema("app")}, `"app".approvals`},
		{[]StoreOption{WithSchema(`we"ird`)}, `"we""ird".approvals`},
	} {
		if got := NewStore(nil, tc.opts...).cfg.table; got != tc.want {
			t.Errorf("table = %s, want %s", got, tc.want)
		}
	}
}

func TestRejectionAndHandoffErrors(t *testing.T) {
	if got := (&Rejection{Reason: "price moved"}).Error(); got != "approval refused on re-check: price moved" {
		t.Errorf("Rejection = %q", got)
	}
	cause := errBoom("down")
	if e := HandoffIncomplete(cause); e.Error() != "approval hand-off incomplete: down" || e.(*HandoffError).Unwrap() != cause {
		t.Errorf("HandoffIncomplete = %v", e)
	}
	if got := HandoffIncomplete(nil).Error(); got != "approval hand-off incomplete" {
		t.Errorf("HandoffIncomplete(nil) = %q", got)
	}
}

type errBoom string

func (e errBoom) Error() string { return string(e) }
