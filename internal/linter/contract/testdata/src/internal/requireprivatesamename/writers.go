package requireprivatesamename // want "package requireprivatesamename has exported methods but no contract.go"

import (
	"io"

	"internal/requireprivatesink"
)

// lineWriter's Write has neither io.Writer's signature nor LineSink's, so it
// implements neither: that both name a Write exempts nothing here, and the
// method is reported.
type lineWriter struct{}

func (lineWriter) Write(line int) error { return nil }

// Use keeps both imports and the type in use.
func Use(w io.Writer, s requireprivatesink.LineSink) error {
	_ = lineWriter{}.Write(1)
	_, _ = w.Write(nil)
	return s.Write("")
}
