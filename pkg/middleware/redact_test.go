package middleware

import (
	"testing"
)

func TestRedact_BasicStruct(t *testing.T) {
	type User struct {
		ID    string
		Email string
		Name  string
	}

	out := Redact(User{ID: "1", Email: "a@b.c", Name: "Alice"}, "Email")
	if out == nil {
		t.Fatal("expected non-nil map")
	}
	if out["ID"] != "1" {
		t.Fatalf("expected ID=1, got %v", out["ID"])
	}
	if out["Email"] != "[REDACTED]" {
		t.Fatalf("expected Email=[REDACTED], got %v", out["Email"])
	}
	if out["Name"] != "Alice" {
		t.Fatalf("expected Name=Alice, got %v", out["Name"])
	}
}

func TestRedact_PointerToStruct(t *testing.T) {
	type Record struct {
		SSN  string
		Name string
	}
	r := &Record{SSN: "123-45-6789", Name: "Bob"}
	out := Redact(r, "SSN")
	if out["SSN"] != "[REDACTED]" {
		t.Fatalf("expected SSN=[REDACTED], got %v", out["SSN"])
	}
	if out["Name"] != "Bob" {
		t.Fatalf("expected Name=Bob, got %v", out["Name"])
	}
}

func TestRedact_NilPointer(t *testing.T) {
	type Foo struct{ X string }
	var f *Foo
	out := Redact(f)
	if out != nil {
		t.Fatalf("expected nil for nil pointer, got %v", out)
	}
}

func TestRedact_NonStruct(t *testing.T) {
	out := Redact("not a struct")
	if out != nil {
		t.Fatalf("expected nil for non-struct, got %v", out)
	}
}

func TestRedact_MultipleFields(t *testing.T) {
	type PII struct {
		Name  string
		Email string
		Phone string
		Age   int
	}
	out := Redact(PII{Name: "N", Email: "E", Phone: "P", Age: 30}, "Email", "Phone")
	if out["Email"] != "[REDACTED]" || out["Phone"] != "[REDACTED]" {
		t.Fatalf("expected both Email and Phone redacted, got %v", out)
	}
	if out["Name"] != "N" || out["Age"] != 30 {
		t.Fatalf("non-redacted fields should be preserved, got %v", out)
	}
}

func TestRedact_NoFieldsSpecified(t *testing.T) {
	type X struct{ A string }
	out := Redact(X{A: "val"})
	if out["A"] != "val" {
		t.Fatalf("with no fields to redact, all should be preserved, got %v", out)
	}
}
