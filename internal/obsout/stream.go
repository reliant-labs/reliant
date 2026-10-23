package obsout

import (
	"fmt"
	"io"
)

// Stream writes NDJSON: one compact JSON record per line, for a verb that
// follows (`logs --follow --json`). A stream ends with [Stream.End], a line
// carrying the same [Head] a one-shot document does, so a consumer learns the
// verdict from the last line rather than from a process it may not own.
type Stream struct {
	w      io.Writer
	schema Schema
	fields []string
}

// NewStream streams records of schema to w, projected to fields (empty = whole
// records).
func NewStream(w io.Writer, schema Schema, fields []string) *Stream {
	return &Stream{w: w, schema: schema, fields: fields}
}

// Record writes one record.
func (s *Stream) Record(rec any) error {
	b, err := s.schema.Project(rec, s.fields)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.w, "%s\n", b)
	return err
}

// End writes the closing envelope line for err (nil = success). It is never
// projected: the verdict is always whole.
func (s *Stream) End(err error) error {
	var h struct {
		Head
		End bool `json:"end"`
	}
	h.Stamp(s.schema.Name, err)
	h.End = true
	b, merr := Schema{Name: s.schema.Name}.Project(h, nil)
	if merr != nil {
		return merr
	}
	_, werr := fmt.Fprintf(s.w, "%s\n", b)
	return werr
}
