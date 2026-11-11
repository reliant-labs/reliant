// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// repairUTF8 replaces every invalid UTF-8 sequence in m's string fields,
// nested messages, lists and maps included, with U+FFFD, and reports whether
// it changed anything.
//
// proto3 refuses to marshal a string field holding invalid UTF-8, so one such
// field anywhere in a DaemonMessage fails the whole Send. Most producers
// sanitize what they put in (sendToolResult does), but the daemon carries text
// from many sources — file names, process output, error strings — and a
// producer that forgets must not take the stream down with it. runSender calls
// this only after a Send failed, so the walk costs nothing on the hot path.
func repairUTF8(m protoreflect.Message) bool {
	type fix struct {
		fd protoreflect.FieldDescriptor
		v  protoreflect.Value
	}
	var fixes []fix
	repaired := false
	// Range must not set fields while it iterates: collect, then apply.
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if repairMap(fd, v.Map()) {
				repaired = true
			}
		case fd.IsList():
			if repairList(fd, v.List()) {
				repaired = true
			}
		case fd.Kind() == protoreflect.StringKind:
			if s := v.String(); !utf8.ValidString(s) {
				fixes = append(fixes, fix{fd, protoreflect.ValueOfString(validUTF8(s))})
			}
		case isMessageKind(fd.Kind()):
			if repairUTF8(v.Message()) {
				repaired = true
			}
		}
		return true
	})
	for _, f := range fixes {
		m.Set(f.fd, f.v)
	}
	return repaired || len(fixes) > 0
}

func repairList(fd protoreflect.FieldDescriptor, l protoreflect.List) bool {
	repaired := false
	for i := 0; i < l.Len(); i++ {
		switch {
		case fd.Kind() == protoreflect.StringKind:
			if s := l.Get(i).String(); !utf8.ValidString(s) {
				l.Set(i, protoreflect.ValueOfString(validUTF8(s)))
				repaired = true
			}
		case isMessageKind(fd.Kind()):
			if repairUTF8(l.Get(i).Message()) {
				repaired = true
			}
		}
	}
	return repaired
}

func repairMap(fd protoreflect.FieldDescriptor, mp protoreflect.Map) bool {
	type entry struct {
		old protoreflect.MapKey
		key protoreflect.MapKey
		v   protoreflect.Value
	}
	var fixes []entry
	repaired := false
	valueKind := fd.MapValue().Kind()
	mp.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		key, keyBad := k, false
		if fd.MapKey().Kind() == protoreflect.StringKind {
			if s := k.String(); !utf8.ValidString(s) {
				key, keyBad = protoreflect.ValueOfString(validUTF8(s)).MapKey(), true
			}
		}
		value, valueBad := v, false
		switch {
		case valueKind == protoreflect.StringKind:
			if s := v.String(); !utf8.ValidString(s) {
				value, valueBad = protoreflect.ValueOfString(validUTF8(s)), true
			}
		case isMessageKind(valueKind):
			// A message value is mutable in place.
			if repairUTF8(v.Message()) {
				repaired = true
			}
		}
		if keyBad || valueBad {
			fixes = append(fixes, entry{old: k, key: key, v: value})
		}
		return true
	})
	for _, f := range fixes {
		mp.Clear(f.old)
		mp.Set(f.key, f.v)
	}
	return repaired || len(fixes) > 0
}

func isMessageKind(k protoreflect.Kind) bool {
	return k == protoreflect.MessageKind || k == protoreflect.GroupKind
}

func validUTF8(s string) string { return strings.ToValidUTF8(s, "\uFFFD") }
