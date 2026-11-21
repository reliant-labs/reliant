package codegen

import (
	"fmt"
	"strings"
	"testing"
)

// This file pins the RUNTIME contract the enum read-path conversion
// (enumFromDBAssign / assignToProto) generates: a stored enum value NAME is
// mapped back to its wire number through pb.<Enum>_value, comma-ok checked.
//
//   - a declared name (incl. <ENUM>_UNSPECIFIED) → its number, no error;
//   - an empty string                            → the zero value, no error
//     (a NULL / absent enum, not corruption);
//   - a NON-EMPTY name absent from the map       → a loud "corrupt enum
//     value" error, NEVER a silent 0/UNSPECIFIED.
//
// fakeStatus stands in for a protoc-gen-go enum; fakeStatusValue is its
// generated _value map (which — like every real one — carries the
// UNSPECIFIED entry at 0). The helpers below reproduce, byte-for-byte in
// behaviour, the code enumFromDBAssign emits for the plain, optional, and
// repeated shapes; the string form of that emit is pinned separately in
// crud_enum_test.go, so together they prove the generated code both READS
// as expected and BEHAVES as expected.

type fakeStatus int32

const (
	fakeStatusUnspecified fakeStatus = 0
	fakeStatusActive      fakeStatus = 1
	fakeStatusClosed      fakeStatus = 2
)

var fakeStatusValue = map[string]int32{
	"STATUS_UNSPECIFIED": 0,
	"STATUS_ACTIVE":      1,
	"STATUS_CLOSED":      2,
}

// plainRead mirrors the generated non-nullable, non-optional read block.
func plainRead(stored string) (fakeStatus, error) {
	var out fakeStatus
	if v, ok := fakeStatusValue[stored]; ok {
		out = fakeStatus(v)
	} else if stored != "" {
		return out, fmt.Errorf("corrupt enum value %q for column status", stored)
	}
	return out, nil
}

// optionalRead mirrors the generated optional (pointer wire field) read block.
func optionalRead(stored string) (*fakeStatus, error) {
	var out *fakeStatus
	if v, ok := fakeStatusValue[stored]; ok {
		ev := fakeStatus(v)
		out = &ev
	} else if stored != "" {
		return nil, fmt.Errorf("corrupt enum value %q for column status", stored)
	}
	return out, nil
}

// repeatedRead mirrors the generated repeated (TEXT[]) read block.
func repeatedRead(stored []string) ([]fakeStatus, error) {
	var out []fakeStatus
	for _, sv := range stored {
		if v, ok := fakeStatusValue[sv]; ok {
			out = append(out, fakeStatus(v))
		} else if sv != "" {
			return nil, fmt.Errorf("corrupt enum value %q for column history", sv)
		}
	}
	return out, nil
}

func TestEnumRead_PlainContract(t *testing.T) {
	cases := []struct {
		name    string
		stored  string
		want    fakeStatus
		wantErr bool
	}{
		{"declared value maps", "STATUS_ACTIVE", fakeStatusActive, false},
		{"unspecified name maps to 0 (not corrupt)", "STATUS_UNSPECIFIED", fakeStatusUnspecified, false},
		{"empty string is absent enum, not corrupt", "", fakeStatusUnspecified, false},
		{"unknown non-empty name is loud", "STATUS_RENAMED", fakeStatusUnspecified, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := plainRead(tc.stored)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("stored=%q: want a corrupt-enum error, got value %v", tc.stored, got)
				}
				if !strings.Contains(err.Error(), "corrupt enum value") || !strings.Contains(err.Error(), tc.stored) {
					t.Fatalf("error should name the corrupt value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("stored=%q: unexpected error %v", tc.stored, err)
			}
			if got != tc.want {
				t.Fatalf("stored=%q: got %v, want %v", tc.stored, got, tc.want)
			}
		})
	}
}

func TestEnumRead_OptionalContract(t *testing.T) {
	// Known → non-nil pointer to the value.
	if got, err := optionalRead("STATUS_CLOSED"); err != nil || got == nil || *got != fakeStatusClosed {
		t.Fatalf("optional known: got %v err %v, want *CLOSED", got, err)
	}
	// Empty → nil pointer, no error (absent optional enum).
	if got, err := optionalRead(""); err != nil || got != nil {
		t.Fatalf("optional empty: got %v err %v, want nil,nil", got, err)
	}
	// Unknown non-empty → loud error.
	if _, err := optionalRead("STATUS_RENAMED"); err == nil {
		t.Fatal("optional unknown non-empty must be a corrupt-enum error, not silent nil")
	}
}

func TestEnumRead_RepeatedContract(t *testing.T) {
	got, err := repeatedRead([]string{"STATUS_ACTIVE", "STATUS_CLOSED", "STATUS_UNSPECIFIED"})
	if err != nil {
		t.Fatalf("repeated known: unexpected error %v", err)
	}
	want := []fakeStatus{fakeStatusActive, fakeStatusClosed, fakeStatusUnspecified}
	if len(got) != len(want) {
		t.Fatalf("repeated known: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("repeated known[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
	// A single corrupt element makes the whole read loud.
	if _, err := repeatedRead([]string{"STATUS_ACTIVE", "STATUS_GONE"}); err == nil {
		t.Fatal("a corrupt element in a repeated enum must be a loud error, not silently UNSPECIFIED")
	}
}
