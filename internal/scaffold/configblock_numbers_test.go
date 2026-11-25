package scaffold

import (
	"testing"
)

func TestNextConfigFieldNumber(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       int
		fail       bool
	}{
		{"same line", "int32 a = 1; string b = 42;", 43, false},
		{"oneof", "oneof choice { string a = 7; int32 b = 98; }", 99, false},
		{"nested", "message Nested { int32 x = 999; } enum E { E_UNSPECIFIED = 0; E_LAST = 800; } int32 a = 2;", 3, false},
		{"reserved", "int32 a = 1; reserved 10, 25 to 30;", 31, false},
		{"reserved implementation tags", "int32 a = 18999;", 20000, false},
		{"comments and strings", "// x = 99999\nint32 a = 3 [description = \"= 77777\"]; /* reserved 500; */", 4, false},
		{"reserved max", "reserved 10 to max;", 0, true},
		{"maximum", "string last = 536870911;", 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := nextConfigFieldNumber(c.body)
			if (err != nil) != c.fail || (!c.fail && got != c.want) {
				t.Fatalf("got %d, %v; want %d, error %v", got, err, c.want, c.fail)
			}
		})
	}
}
