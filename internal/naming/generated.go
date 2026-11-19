package naming

// Names of GENERATED symbols — the identifiers protoc-gen-go,
// protoc-gen-connect-go and protoc-gen-es declare for a proto name.
//
// Forge does not choose these. Every site that references a generated
// symbol (`pb.Foo`, `req.FooBar`, `foov1connect.UnimplementedFooServiceHandler`,
// `client.listFoos`, `FooSchema`) must spell it exactly as the generator
// did, or the scaffold does not compile. So these functions are PORTS of
// the generators' own rules, not forge conventions, and must never be
// "improved": a nicer spelling here is a compile error in every project.
//
// The rules differ in exactly the places a hand-rolled title-caser gets
// wrong — a letter after a digit, an underscore before a digit, a leading
// underscore, a lowercase letter inside an acronym run — so a name that
// is a field, message, enum, service or method in a .proto goes through
// these and nothing else. TestGeneratedNames_MatchGenerators pins each one
// against the real generators.

import "strings"

// GoCamelCase is protoc-gen-go's camel-casing of a proto name into a Go
// identifier — a verbatim port of google.golang.org/protobuf's
// internal/strs.GoCamelCase, which protogen applies to every message,
// enum, field, oneof, service and method name. protoc-gen-connect-go
// builds its identifiers from the same result (see ConnectServiceGoName,
// and ConnectServiceNameConst for its one exception).
//
// Words begin at an underscore or an upper-case letter, and a digit is a
// word of its own, so the letter AFTER a digit is capitalised:
//
//	base64item     → Base64Item     (forge's old rule said Base64item)
//	alphav1connect → Alphav1Connect
//	address_line_2 → AddressLine_2  (an underscore before a non-lowercase
//	                                 byte is kept)
//	_internal      → XInternal      (a leading underscore becomes X)
//	HTTPStatus     → HTTPStatus     (no initialism folding)
//
// For a NESTED message or enum pass the name relative to its package with
// the dots kept ("Outer.Inner" → "Outer_Inner"): that is the string
// protogen camel-cases, and the dot handling below is what makes nested
// types come out joined by an underscore.
func GoCamelCase(s string) string {
	// Invariant: if the next letter is lower case, it must be converted
	// to upper case.
	// That is, we process a word at a time, where words are marked by _ or
	// upper case letter. Digits are treated as words.
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.' && i+1 < len(s) && isASCIILower(s[i+1]):
			// Skip over '.' in ".{{lowercase}}".
		case c == '.':
			b = append(b, '_') // convert '.' to '_'
		case c == '_' && (i == 0 || s[i-1] == '.'):
			// Convert initial '_' to ensure we start with a capital letter.
			// Do the same for '_' after '.' to match historic behavior.
			b = append(b, 'X') // convert '_' to 'X'
		case c == '_' && i+1 < len(s) && isASCIILower(s[i+1]):
			// Skip over '_' in "_{{lowercase}}".
		case isASCIIDigit(c):
			b = append(b, c)
		default:
			// Assume we have a letter now - if not, it's a bogus identifier.
			// The next word is a sequence of characters that must start upper case.
			if isASCIILower(c) {
				c -= 'a' - 'A' // convert lowercase to uppercase
			}
			b = append(b, c)

			// Accept lower case sequence that follows.
			for ; i+1 < len(s) && isASCIILower(s[i+1]); i++ {
				b = append(b, s[i+1])
			}
		}
	}
	return string(b)
}

// GoTypeName is the Go identifier protoc-gen-go declares for a message or
// enum, given its fully-qualified proto name and the proto package of the
// file declaring it: "shop.v1.Order.Status" in "shop.v1" → "Order_Status".
// This is protogen's newGoIdent. A name outside protoPkg (or an empty
// package) is camel-cased whole, as protogen would for a package-less file.
func GoTypeName(protoPkg, fullName string) string {
	if protoPkg != "" {
		fullName = strings.TrimPrefix(fullName, protoPkg+".")
	}
	return GoCamelCase(fullName)
}

// goReservedFieldNames are the method names every generated message
// already has. protogen starts its per-message name table with them, so
// a field whose camel-cased name lands on one is renamed.
var goReservedFieldNames = []string{
	"Reset",
	"String",
	"ProtoMessage",
	"Marshal",
	"Unmarshal",
	"ExtensionRangeArray",
	"ExtensionMap",
	"Descriptor",
}

// GoFieldNames returns the Go struct field names protoc-gen-go generates
// for one message's fields, given every proto field name of that message
// in DECLARATION order.
//
// It is GoCamelCase plus protogen's per-message conflict resolution: a
// field that would collide with a generated method (`descriptor` →
// Descriptor, `string` → String) or with an earlier field or its getter
// (`get_name` after `name`, whose getter is GetName) gets "_" appended
// until it is unique. Because that depends on the fields declared before
// it, a single field's Go name is only exact when computed from the whole
// list — which is why this takes the list rather than one name.
//
// A real (non-synthetic) oneof also claims its own camel-cased name in
// the same table, after its first member. forge's generated surfaces
// never reference oneof groups; callers holding one should not assume
// this covers it.
func GoFieldNames(protoFieldNames []string) []string {
	used := make(map[string]bool, len(goReservedFieldNames)+2*len(protoFieldNames))
	for _, n := range goReservedFieldNames {
		used[n] = true
	}
	out := make([]string, len(protoFieldNames))
	for i, f := range protoFieldNames {
		name := GoCamelCase(f)
		for used[name] || used["Get"+name] {
			name += "_"
		}
		used[name] = true
		used["Get"+name] = true
		out[i] = name
	}
	return out
}

// GoFieldName is GoFieldNames for the field called protoFieldName among
// siblings (the message's proto field names in declaration order). When
// protoFieldName is not among them — a caller that knows only the one
// field — it falls back to GoCamelCase, which is exact unless the field
// collides with a generated method or another field.
func GoFieldName(protoFieldName string, siblings []string) string {
	for i, s := range siblings {
		if s == protoFieldName {
			return GoFieldNames(siblings)[i]
		}
	}
	return GoFieldNames([]string{protoFieldName})[0]
}

// ConnectPackage is the Go package protoc-gen-connect-go writes a file's
// services into: the file's Go package name plus the default
// package_suffix "connect" ("billingv1" → "billingv1connect").
func ConnectPackage(goPackageName string) string {
	return goPackageName + "connect"
}

// ConnectServiceGoName is the base protoc-gen-connect-go builds a
// service's identifiers from — the service's protogen GoName, i.e.
// GoCamelCase of the proto name. It declares `<base>Handler`,
// `Unimplemented<base>Handler`, `New<base>Handler`, `<base>Client` and
// `New<base>Client`: "Alphav1connectService" →
// Alphav1ConnectServiceHandler. Templates take this base and add the
// affix, exactly as the generator does.
//
// The `…Name` constant is the one exception — see ConnectServiceNameConst.
func ConnectServiceGoName(protoService string) string {
	return GoCamelCase(protoService)
}

// ConnectServiceNameConst is the constant protoc-gen-connect-go declares
// holding a service's fully-qualified name. Unlike every other identifier
// it declares, it is spelled from the RAW proto service name, not the
// GoName: "Alphav1connectService" → Alphav1connectServiceName (while the
// handler is Alphav1ConnectServiceHandler).
func ConnectServiceNameConst(protoService string) string {
	return protoService + "Name"
}

// ConnectProcedureConst is the `<Service><Method>Procedure` constant
// protoc-gen-connect-go declares for one RPC — both halves GoNames.
func ConnectProcedureConst(protoService, protoMethod string) string {
	return GoCamelCase(protoService) + GoCamelCase(protoMethod) + "Procedure"
}

// ProtoCamelCase is protobuf-es's protoCamelCase — the lowerCamelCase a
// field or oneof name takes in generated TypeScript (and protoc's default
// json_name). Unlike GoCamelCase it never capitalises after a digit and
// never keeps an underscore: "base64item" → "base64item",
// "address_line_2" → "addressLine2", "foo_bar" → "fooBar". The first
// letter keeps its case. Port of @bufbuild/protobuf reflect/names.ts.
func ProtoCamelCase(snakeCase string) string {
	capNext := false
	b := make([]byte, 0, len(snakeCase))
	for i := 0; i < len(snakeCase); i++ {
		c := snakeCase[i]
		switch {
		case c == '_':
			capNext = true
		case isASCIIDigit(c):
			b = append(b, c)
			capNext = false
		default:
			if capNext {
				capNext = false
				if isASCIILower(c) {
					c -= 'a' - 'A'
				}
			}
			b = append(b, c)
		}
	}
	return string(b)
}

// esReservedObjectProperties are the property names protobuf-es escapes
// with a trailing "$" (safeObjectProperty) because they shadow built-in
// JavaScript object properties.
var esReservedObjectProperties = map[string]bool{
	"constructor": true,
	"toString":    true,
	"toJSON":      true,
	"valueOf":     true,
}

// esSafeObjectProperty is protobuf-es's safeObjectProperty.
func esSafeObjectProperty(name string) string {
	if esReservedObjectProperties[name] {
		return name + "$"
	}
	return name
}

// EsFieldName is the property protoc-gen-es generates for a (non-oneof)
// message field — its localName: ProtoCamelCase, then the reserved
// property escape ("to_string" → "toString$"). Every TS reference to a
// generated message field (`item.fooBar`, `create(Schema, { fooBar })`)
// must use this.
func EsFieldName(protoFieldName string) string {
	return esSafeObjectProperty(ProtoCamelCase(protoFieldName))
}

// EsMethodName is the localName protobuf-es gives an RPC — the key on a
// connect-es client (`client.listFoos`) and on a service's `method` map.
// It lower-cases the FIRST CHARACTER ONLY: "LLMChat" → "lLMChat", not
// "llmChat". A camel-caser that lowers a leading acronym run names a
// method the client does not have.
func EsMethodName(protoMethodName string) string {
	if protoMethodName == "" {
		return ""
	}
	return esSafeObjectProperty(strings.ToLower(protoMethodName[:1]) + protoMethodName[1:])
}

func isASCIILower(c byte) bool { return 'a' <= c && c <= 'z' }

func isASCIIDigit(c byte) bool { return '0' <= c && c <= '9' }
