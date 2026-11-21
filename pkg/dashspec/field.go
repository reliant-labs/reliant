package dashspec

import (
	"fmt"
	"strings"
)

// maxAttrKeyLen bounds the attribute key of a span.attr., log.attr.,
// metric.attr. or resource.attr. field.
const maxAttrKeyLen = 128

// CheckField reports whether field is a semantic field on signal (the
// vocabulary documented on FieldService). Every renderer maps exactly these
// fields, so a field CheckField accepts is one each can draw.
func CheckField(sig Signal, field string) error {
	switch {
	case field == FieldService, field == FieldEnv:
		return nil
	case strings.HasPrefix(field, "resource.attr."):
		return attrKey(field, "resource.attr.")
	}
	switch sig {
	case Traces:
		switch field {
		case "timestamp", "span.name", "span.kind", "span.status", "span.status_message", "span.duration":
			return nil
		}
		if strings.HasPrefix(field, "span.attr.") {
			return attrKey(field, "span.attr.")
		}
	case Logs:
		switch field {
		case "timestamp", "log.severity", "log.body":
			return nil
		}
		if strings.HasPrefix(field, "log.attr.") {
			return attrKey(field, "log.attr.")
		}
	case Metrics:
		if strings.HasPrefix(field, "metric.attr.") {
			return attrKey(field, "metric.attr.")
		}
	}
	return fmt.Errorf("field %q is not valid on %s", field, sig)
}

// attrKey checks the key after an attribute field's prefix.
func attrKey(field, prefix string) error {
	if k := strings.TrimPrefix(field, prefix); k == "" || len(k) > maxAttrKeyLen {
		return fmt.Errorf("field %q needs an attribute key of 1 to %d characters", field, maxAttrKeyLen)
	}
	return nil
}
