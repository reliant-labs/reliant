package obsout

import (
	"io"

	"github.com/spf13/pflag"
)

// Flags are the output flags every agent verb registers the same way:
// --json, --fields and --limit. A verb that streams also honours --json as
// NDJSON.
type Flags struct {
	JSON   bool
	Fields string
	Limit  int
}

// Register adds the flags to fs. defaultLimit is the verb's own page size.
func (f *Flags) Register(fs *pflag.FlagSet, defaultLimit int) {
	fs.BoolVar(&f.JSON, "json", false, "print one JSON document on stdout (see the schema below; progress goes to stderr)")
	fs.StringVar(&f.Fields, "fields", "", "with --json: keep only these comma-separated dotted fields (the head is always kept)")
	fs.IntVar(&f.Limit, "limit", defaultLimit, "most items to return")
}

// Parsed validates --fields against schema.
func (f Flags) Parsed(schema Schema) ([]string, error) {
	return schema.ParseFields(f.Fields)
}

// Emit writes doc as JSON (honouring --fields) when --json is set and reports
// true; a false return means the caller renders the human form.
func (f Flags) Emit(w io.Writer, schema Schema, doc any, fields []string) (bool, error) {
	if !f.JSON {
		return false, nil
	}
	return true, schema.Write(w, doc, fields)
}
