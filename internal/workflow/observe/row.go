// Copyright (c) 2026 Reliant Labs
package wfobserve

import (
	"encoding/json"
)

// rowStringLimits are the string cuts a row too large for MaxRowBytes is
// retried at, longest first, before trailing events are dropped.
var rowStringLimits = []int{MaxStoredStringBytes, 128, 32}

// Row is a result as a node_observations row stores it: the attributes, the
// events and the errors as JSON text, and whether anything was cut. The
// attributes and events together fit MaxRowBytes: long strings are cut first,
// then trailing events are dropped, then the attributes are replaced with a
// marker (design section 8.1).
type Row struct {
	Attributes string
	Events     string
	Errors     string
	Truncated  bool
}

// Row renders r for storage.
func (r Result) Row() Row {
	out := Row{Truncated: r.Truncated}
	attrs := map[string]interface{}(r.Attributes)
	if attrs == nil {
		attrs = map[string]interface{}{}
	}
	events := r.Events
	if events == nil {
		events = []Event{}
	}

	fit := func(a map[string]interface{}, e []Event) (string, string, bool) {
		ab, err1 := json.Marshal(a)
		eb, err2 := json.Marshal(e)
		if err1 != nil || err2 != nil {
			return "", "", false
		}
		return string(ab), string(eb), len(ab)+len(eb) <= MaxRowBytes
	}

	done := false
	for i, limit := range rowStringLimits {
		a, e := attrs, events
		if i > 0 {
			a, e = cutAttributes(attrs, limit), cutEvents(events, limit)
		}
		if as, es, ok := fit(a, e); ok {
			out.Attributes, out.Events = as, es
			out.Truncated = out.Truncated || i > 0
			done = true
			break
		}
	}
	if !done {
		short := rowStringLimits[len(rowStringLimits)-1]
		a, e := cutAttributes(attrs, short), cutEvents(events, short)
		for len(e) > 0 {
			e = e[:len(e)-1]
			if as, es, ok := fit(a, e); ok {
				out.Attributes, out.Events, done = as, es, true
				break
			}
		}
		if !done {
			as, es, ok := fit(a, []Event{})
			if !ok {
				as = `{"_truncated":"too large to record"}`
			}
			out.Attributes, out.Events = as, es
			if es == "" {
				out.Events = "[]"
			}
		}
		out.Truncated = true
	}

	msgs := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		msgs = append(msgs, e.Message)
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		b = []byte("[]")
	}
	out.Errors = string(b)
	return out
}

func cutAttributes(m map[string]interface{}, limit int) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			v = cutString(s, limit)
		}
		out[k] = v
	}
	return out
}

func cutEvents(events []Event, limit int) []Event {
	out := make([]Event, len(events))
	for i, ev := range events {
		out[i] = Event{Name: ev.Name}
		if ev.Attributes != nil {
			out[i].Attributes = cutAttributes(ev.Attributes, limit)
		}
	}
	return out
}
