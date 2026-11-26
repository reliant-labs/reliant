package config

import (
	"fmt"
	"regexp"
	"strings"
)

// ParseInfraPatterns reads a gate.infra_patterns_file: one regular
// expression (Go RE2) per line. Surrounding whitespace is trimmed, and blank
// lines and lines starting with # are skipped. The error, when not nil, is an
// [Errors] naming each bad line.
func ParseInfraPatterns(data []byte) ([]*regexp.Regexp, error) {
	var (
		out  []*regexp.Regexp
		errs Errors
	)
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			errs = append(errs, &FieldError{Line: i + 1, Msg: fmt.Sprintf("not a valid regular expression: %v", err)})
			continue
		}
		out = append(out, re)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}
