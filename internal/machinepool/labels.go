// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"fmt"
	"regexp"
	"strings"
)

// labelSlug is what a label key or value may be: a short slug, so a label
// reads the same in a selector, a URL and a log line.
var labelSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// MaxLabels bounds how many labels one machine carries.
const MaxLabels = 32

// IsSlug reports whether s is a valid label key or value, which is also the
// rule for a pool's name.
func IsSlug(s string) bool { return labelSlug.MatchString(s) }

// ValidateLabels refuses labels that are not short slugs, or more than
// MaxLabels of them. what names the labels in the error ("labels",
// "selector").
func ValidateLabels(labels map[string]string, what string) error {
	if len(labels) > MaxLabels {
		return fmt.Errorf("%s: at most %d labels", what, MaxLabels)
	}
	for k, v := range labels {
		if !labelSlug.MatchString(k) || !labelSlug.MatchString(v) {
			return fmt.Errorf("%s: %q=%q: keys and values are lowercase slugs (a-z, 0-9, '.', '_', '-'), at most 63 characters", what, k, v)
		}
	}
	return nil
}

// ParseLabels reads "key=value" arguments, each of which may hold several
// comma-separated pairs ("pool=pipeline,gate=yes"), into a label map and
// validates it. A key given twice takes its last value. Empty entries (a
// trailing comma) are skipped; nil is returned for no labels at all.
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
