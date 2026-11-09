// Minimal, fully typed boundary API for analysistest. This is a fixture, not
// an importer stub: the Go analysis driver parses and type-checks it normally.
package yaml

func Marshal(v any) ([]byte, error)      { return nil, nil }
func Unmarshal(data []byte, v any) error { return nil }

type Encoder struct{}
type Decoder struct{}

func NewEncoder(w any) *Encoder     { return nil }
func NewDecoder(r any) *Decoder     { return nil }
func (*Encoder) Encode(v any) error { return nil }
func (*Decoder) Decode(v any) error { return nil }
