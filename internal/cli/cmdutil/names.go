package cmdutil

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/forge/internal/naming"
)

// The name-validation rules below are shared by `forge project new` (which stays in
// internal/cli) and the dir-nested `forge scaffold` group (internal/cli/scaffold).
// They live here in the shared leaf package so both reach one
// implementation without an import cycle (internal/cli blank-imports the
// groups, so the groups cannot import internal/cli). The behavior is
// byte-for-byte the historic internal/cli implementation — same error
// strings, same rule order.

// GoKeywords is the set of Go reserved keywords.
var GoKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// GoPredeclaredIdentifiers is the set of Go predeclared types, constants,
// zero value, and builtin functions.
var GoPredeclaredIdentifiers = map[string]bool{
	// Types
	"bool": true, "byte": true, "complex64": true, "complex128": true,
	"error": true, "float32": true, "float64": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"rune": true, "string": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"any": true, "comparable": true,
	// Constants
	"true": true, "false": true, "iota": true,
	// Zero value
	"nil": true,
	// Builtin functions
	"append": true, "cap": true, "close": true, "complex": true, "copy": true,
	"delete": true, "imag": true, "len": true, "make": true, "new": true,
	"panic": true, "print": true, "println": true, "real": true, "recover": true,
	"min": true, "max": true, "clear": true,
}

// ReservedServiceNames are names that conflict with forge's worker/scheduler
// subsystems. Using them as HTTP Connect service names causes confusion.
var ReservedServiceNames = map[string]bool{
	"worker": true, "scheduler": true, "cron": true, "job": true,
}

// ReservedServiceNameList returns the reserved names in stable order, for
// error messages and help text. Nothing else enumerates them, and a name
// that is rejected without the set being visible sends people guessing —
// `job` in particular is the natural noun for a whole class of domains
// (field service, construction, scheduling, print, logistics).
func ReservedServiceNameList() []string {
	out := make([]string, 0, len(ReservedServiceNames))
	for name := range ReservedServiceNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// reservedNameAlternatives suggests service names for a rejected reserved
// noun. Concrete suggestions matter more here than the general rule: the
// author has a domain concept in mind and needs a name for it now, and
// "pick something else" sends them to invent one that may collide again.
func reservedNameAlternatives(name string) string {
	switch name {
	case "job":
		return "`workorder`, `dispatch`, or `booking`"
	case "worker":
		return "`crew`, `staff`, or `operator_pool`"
	case "scheduler", "cron":
		return "`schedule`, `itinerary`, or `calendar`"
	default:
		return "a noun naming what the service owns"
	}
}

// ValidateServiceName checks that a name is valid for a service and not a
// reserved service name. For background workers use 'forge scaffold worker <name>'.
func ValidateServiceName(name string) error {
	if err := ValidateIdentifier(name); err != nil {
		return err
	}
	if err := ValidateImportablePackageName(name); err != nil {
		return err
	}
	if ReservedServiceNames[strings.ToLower(name)] {
		return fmt.Errorf("%q is reserved (reserved service names: %s) — these collide with forge's "+
			"worker/scheduler subsystems.\n"+
			"  For a background worker:  forge scaffold worker %s\n"+
			"  For a DOMAIN entity of this name — %q is the natural noun in field service, "+
			"construction, scheduling and logistics — name the service for what it owns "+
			"instead (e.g. %s). The service is the bounded context, not the table: its "+
			"entity messages can still be called %s",
			name, strings.Join(ReservedServiceNameList(), ", "), name, name,
			reservedNameAlternatives(strings.ToLower(name)), naming.ToPascalCase(name))
	}
	return nil
}

// ValidateImportablePackageName rejects a component name whose Go package
// cannot be imported. A service, worker, operator or library becomes a
// package that other code imports — internal/app imports every component —
// and Go refuses to import package main ("is a program, not an importable
// package"). Without this check the name is accepted and the project fails
// at go build, far from the command that chose it. This is a Go language
// rule, not a forge reservation, so it is the only name checked here.
func ValidateImportablePackageName(name string) error {
	if naming.ServicePackage(name) == "main" {
		return fmt.Errorf("%q would be Go package main, which is a program and cannot be imported — "+
			"forge imports every service, worker and operator into internal/app; "+
			"name the component for what it owns instead", name)
	}
	return nil
}

// ValidateServiceDirConsistency checks logical service identity, not Go casing.
// Connect identifiers are derived from the authoritative proto spelling; Forge's
// own Mount/NewTest names are independent, idiomatic Go identifiers. Separators,
// word boundaries and initialism casing therefore need not match.
func ValidateServiceDirConsistency(protoServiceName, dirName string) error {
	base := strings.TrimSuffix(protoServiceName, "Service")
	fold := func(s string) string {
		return strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s))
	}
	if fold(base) == fold(dirName) {
		return nil
	}
	return fmt.Errorf("proto service %q does not match directory %q: Fix: rename the proto service to %q, or rename the directory to %q",
		protoServiceName, dirName, naming.ToPascalCase(dirName)+"Service", naming.ToSnakeCase(base))
}

// ValidateIdentifier checks that a name is valid for use as a service,
// worker, or operator name. Hyphens and underscores are allowed in the
// display name; templates use snakeCase/pascalCase helpers to convert when a
// Go identifier is needed (e.g. "admin-server" / "admin_server" -> package
// "admin_server" and field "AdminServer" — snake_case is the canonical
// on-disk form post-2026-06-08). The leading-character and reserved-word
// rules match ValidateProjectName so all top-level scaffold names share one
// shape.
func ValidateIdentifier(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	first, _ := utf8.DecodeRuneInString(name)
	if !unicode.IsLetter(first) {
		return fmt.Errorf("name must start with a letter")
	}
	last, _ := utf8.DecodeLastRuneInString(name)
	if last == '-' {
		return fmt.Errorf("name cannot end with a hyphen")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return fmt.Errorf("name contains invalid character: %c", r)
		}
	}
	if GoKeywords[name] {
		return fmt.Errorf("%q is a Go keyword", name)
	}
	if GoPredeclaredIdentifiers[name] {
		return fmt.Errorf("%q is a Go predeclared identifier", name)
	}
	return nil
}

// ValidateProjectName checks that a project name is valid for use as a
// directory name and in Go module paths. Hyphens are allowed since they are
// valid in module paths and directory names; templates use
// snakeCase/pascalCase helpers to convert when a Go identifier is needed.
func ValidateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	first, _ := utf8.DecodeRuneInString(name)
	if !unicode.IsLetter(first) {
		return fmt.Errorf("name must start with a letter")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return fmt.Errorf("name contains invalid character: %c", r)
		}
	}
	if GoKeywords[name] {
		return fmt.Errorf("%q is a Go keyword", name)
	}
	if GoPredeclaredIdentifiers[name] {
		return fmt.Errorf("%q is a Go predeclared identifier", name)
	}
	return nil
}

// ValidateFrontendName checks that a frontend name is filesystem-safe.
func ValidateFrontendName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if strings.ContainsAny(name, `/\:*?"<>|`) {
		return fmt.Errorf("name contains invalid filesystem characters")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("name cannot contain spaces")
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return fmt.Errorf("name cannot start with . or -")
	}
	return nil
}
