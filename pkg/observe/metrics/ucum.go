package metrics

import "strings"

// ucumPlain are atoms that take no prefix.
var ucumPlain = map[string]bool{
	"%": true, "min": true, "h": true, "d": true, "wk": true, "mo": true, "a": true,
	"deg": true, "rad": true, "Cel": true, "[in_i]": true, "[ft_i]": true, "[mi_i]": true,
	"[pH]": true, "[ppb]": true, "[ppm]": true,
}

// ucumPrefixable are atoms a metric prefix may precede.
var ucumPrefixable = map[string]bool{
	"s": true, "m": true, "g": true, "L": true, "l": true, "Hz": true, "K": true,
	"J": true, "W": true, "V": true, "A": true, "Pa": true, "N": true, "mol": true,
	"By": true, "bit": true, "B": true, "Bd": true,
}

var ucumPrefixes = []string{"Y", "Z", "E", "P", "T", "G", "M", "k", "h", "da", "d", "c", "m", "u", "n", "p", "f", "a", "z", "y"}

var ucumBinaryPrefixes = []string{"Ki", "Mi", "Gi", "Ti", "Pi", "Ei"}

// knownAtom reports whether a is a UCUM atom from the supported subset,
// optionally behind a metric prefix (binary prefixes only for By and bit).
func knownAtom(a string) bool {
	if ucumPlain[a] || ucumPrefixable[a] {
		return true
	}
	for _, p := range ucumBinaryPrefixes {
		if rest, ok := strings.CutPrefix(a, p); ok && (rest == "By" || rest == "bit") {
			return true
		}
	}
	for _, p := range ucumPrefixes {
		if rest, ok := strings.CutPrefix(a, p); ok && ucumPrefixable[rest] {
			return true
		}
	}
	return false
}
