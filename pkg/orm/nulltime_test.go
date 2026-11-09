package orm

import (
	"testing"
	"time"
)

func TestNullTime_Scan(t *testing.T) {
	ref := time.Date(2026, 6, 11, 14, 30, 5, 0, time.UTC)

	tests := []struct {
		name      string
		value     any
		wantValid bool
		wantTime  time.Time
		wantErr   bool
	}{
		{name: "nil is NULL", value: nil, wantValid: false},
		{name: "time.Time passthrough", value: ref, wantValid: true, wantTime: ref},
		{name: "sqlite datetime string", value: "2026-06-11 14:30:05", wantValid: true, wantTime: ref},
		{name: "sqlite datetime bytes", value: []byte("2026-06-11 14:30:05"), wantValid: true, wantTime: ref},
		{name: "sqlite fractional with offset", value: "2026-06-11 14:30:05.000000000+00:00", wantValid: true, wantTime: ref},
		{name: "rfc3339", value: "2026-06-11T14:30:05Z", wantValid: true, wantTime: ref},
		{name: "date only", value: "2026-06-11", wantValid: true, wantTime: time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)},
		{name: "empty string is NULL", value: "", wantValid: false},
		{name: "garbage string errors", value: "not-a-time", wantErr: true},
		{name: "unsupported type errors", value: 42, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n NullTime
			err := n.Scan(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Scan(%v) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if n.Valid != tt.wantValid {
				t.Fatalf("Scan(%v) Valid = %v, want %v", tt.value, n.Valid, tt.wantValid)
			}
			if tt.wantValid && !n.Time.Equal(tt.wantTime) {
				t.Fatalf("Scan(%v) Time = %v, want %v", tt.value, n.Time, tt.wantTime)
			}
		})
	}
}

func TestNullTime_ScanResetsPriorState(t *testing.T) {
	n := NullTime{Time: time.Now(), Valid: true}
	if err := n.Scan(nil); err != nil {
		t.Fatalf("Scan(nil) error = %v", err)
	}
	if n.Valid || !n.Time.IsZero() {
		t.Fatalf("Scan(nil) should reset state, got %+v", n)
	}
}

func TestNullTime_Value(t *testing.T) {
	var n NullTime
	v, err := n.Value()
	if err != nil || v != nil {
		t.Fatalf("invalid NullTime should Value() to nil, got %v, %v", v, err)
	}

	ref := time.Date(2026, 6, 11, 14, 30, 5, 0, time.UTC)
	n = NullTime{Time: ref, Valid: true}
	v, err = n.Value()
	if err != nil {
		t.Fatalf("Value() error = %v", err)
	}
	got, ok := v.(time.Time)
	if !ok || !got.Equal(ref) {
		t.Fatalf("Value() = %v, want %v", v, ref)
	}
}
