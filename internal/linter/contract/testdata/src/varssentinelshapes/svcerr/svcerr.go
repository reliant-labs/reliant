// Package svcerr stands in for forge/pkg/svcerr inside the analysistest
// GOPATH: the shared sentinel set plus the constructors the service-layer
// and api skills tell authors to use instead of re-rolling their own.
package svcerr

import "errors"

var ErrNotFound = errors.New("not found")
var ErrResourceExhausted = errors.New("resource exhausted")

// ResourceExhausted wraps the shared sentinel, exactly as the real one does.
func ResourceExhausted(what string) error {
	return errors.Join(ErrResourceExhausted, errors.New(what))
}
