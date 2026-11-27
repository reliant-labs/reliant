// Package requireprivatesamenameok: two private types, each implementing a
// different imported interface whose one method is named Write (io.Writer's
// takes bytes, requireprivatesink.LineSink's a string). Each Write is exempt
// on the type that implements its interface: no contract.go is required.
package requireprivatesamenameok

import (
	"io"

	"internal/requireprivatesink"
)

type byteWriter struct{}

func (byteWriter) Write(p []byte) (int, error) { return len(p), nil }

type lineWriter struct{}

func (lineWriter) Write(line string) error { return nil }

// Sinks returns both, through the imported interfaces only.
func Sinks() (io.Writer, requireprivatesink.LineSink) { return byteWriter{}, lineWriter{} }
