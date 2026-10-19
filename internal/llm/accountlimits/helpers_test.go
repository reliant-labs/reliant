// Copyright (c) 2025 Reliant Labs

package accountlimits_test

import (
	"io"
	"net/http"
	"strings"
)

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func readCloser(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }
