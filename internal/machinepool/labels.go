// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"fmt"
	"strings"
)

// IsSlug reports whether s is a valid label key or value, which is also the
// rule for a pool's name (labelSlug, spec.go).
func IsSlug(s string) bool { return labelSlug.MatchString(s) }

// ParseLabels reads "key=value" arguments, each of which may hold several
// comma-separated pairs ("pool=pipeline,gate=yes"), into a label map and
// checks it with ValidateLabels. A key given twice takes its last value.
// Empty entries (a trailing comma) are skipped; nil is returned for no labels
// at all. what names the labels in an error ("--label").
func ParseLabels(args []string, what string) (map[string]string, error) {
	var out map[string]string
	for _, arg := range args {
		for _, pair := range strings.Split(arg, ",") {
			pair = strings.TrimSpace(pair)
			if pair == "" {
				continue
			}
			k, v, ok := strings.Cut(pair, "=")
			if !ok {
				return nil, fmt.Errorf("%s: %q is not key=value", what, pair)
			}
			if out == nil {
				out = map[string]string{}
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if err := ValidateLabels(out, what); err != nil {
		return nil, err
	}
	return out, nil
}
