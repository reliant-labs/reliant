// Copyright (c) 2025 Reliant Labs
package ptr

// Of returns a pointer to v.
func Of[T any](v T) *T {
	return &v
}

// From dereferences p, returning the zero value of T if p is nil.
func From[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// StringIfNotEmpty returns a pointer to s if s is not empty, otherwise nil.
func StringIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// IntIfPositive returns a pointer to i if i > 0, otherwise nil.
func IntIfPositive(i int) *int {
	if i > 0 {
		return &i
	}
	return nil
}
