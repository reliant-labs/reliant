package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/scanner"

	"github.com/reliant-labs/forge/internal/codegen"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ConfigRootMessage is the config message a scaffolded component's block is
// composed on: the project-global config every scaffold declares in
// proto/<project>/config/v1/config.proto, which pkg/config's `Config` aliases and
// internal/app/compose.go reads blocks off (infra.Cfg).
const ConfigRootMessage = "AppConfig"

// ErrNoConfigRoot reports that the project has no config-directory message
// named ConfigRootMessage to compose a block on: it has no typed config, or
// it moved entirely to per-binary configs. The caller refuses before writing
// adapter files and explains how to declare the required config root.
var ErrNoConfigRoot = errors.New("no " + ConfigRootMessage + " message in the config proto directory")

// ConfigBlockSpec is one component config block: a `message <Message>` of
// annotated leaves, composed on the root config as `<Message> <Field> = N;`.
type ConfigBlockSpec struct {
	// Message is the block's message name, e.g. "AlpacaConfig".
	Message string
	// Field is the root config field holding it, e.g. "alpaca" (its Go
	// getter is GetAlpaca).
	Field string
	// Doc is the comment written above the message, one line per element.
	Doc []string
	// FieldDoc is the one-line comment written above the root field.
	FieldDoc string
	// Leaves are the block's fields, in declaration order.
	Leaves []ConfigBlockLeaf
}

// ConfigBlockLeaf is one annotated scalar leaf of a block.
type ConfigBlockLeaf struct {
	Type        string // proto scalar type, e.g. "string"
	Name        string // proto field name, e.g. "base_url"
	EnvVar      string // (forge.v1.config).env_var
	Description string // (forge.v1.config).description
}

// ConfigBlockResult reports what EnsureConfigBlock changed.
type ConfigBlockResult struct {
	// File is the .proto file that declares the root config (and, when it
	// was added, the block).
	File string
	// AddedMessage / AddedField report the two halves this call wrote.
	// Both false is the idempotent no-op: the block was already declared
	// and composed.
	AddedMessage bool
	AddedField   bool
	// FieldNumber is the root config field number the block was given when
	// AddedField; zero otherwise.
	FieldNumber int
}

// EnsureConfigBlock makes the proto config in configDir (proto/<project>/config/v1)
// declare spec's block and compose it on ConfigRootMessage, writing only the
// half that is missing. It is a birth-time splice into a file the author owns,
// held to the same invariants as the managed-field injection
// (entity_managed_fields.go in internal/cli/scaffold):
//
//   - IDEMPOTENT. A block message that is already declared anywhere in the
//     package is never re-declared, and a root that already composes it is
//     never given a second field: re-running the scaffold changes nothing.
//   - NEVER RENUMBERS. The root field takes the root message's high-water
//     mark + 1, and that mark counts `reserved` numbers, so it can neither
//     collide with nor reuse a number the author wrote or retired.
//   - REFUSES RATHER THAN GUESSES. A root field already named spec.Field
//     with another type, an existing block message lacking a leaf the
//     scaffold's code reads, or a leaf env var another field already binds
//     is an error naming the conflict; the file is left untouched.
//
// It returns ErrNoConfigRoot when there is no root message to compose on.
func EnsureConfigBlock(configDir string, spec ConfigBlockSpec) (ConfigBlockResult, error) {
	return ensureConfigBlock(configDir, spec, true)
}

// PlanConfigBlock is EnsureConfigBlock without the write: the same checks and
// refusals, and a result reporting what EnsureConfigBlock WOULD add (and the
// field number it would take). A scaffold's --dry-run reports it.
func PlanConfigBlock(configDir string, spec ConfigBlockSpec) (ConfigBlockResult, error) {
	return ensureConfigBlock(configDir, spec, false)
}

func ensureConfigBlock(configDir string, spec ConfigBlockSpec, write bool) (ConfigBlockResult, error) {
	scan, err := codegen.ScanRawProtoDir(configDir) // a missing dir scans empty
	if err != nil {
		return ConfigBlockResult{}, err
	}
	var root *codegen.RawProtoMessage
	var block *codegen.RawProtoMessage
	for i := range scan.Messages {
		switch scan.Messages[i].Name {
		case ConfigRootMessage:
			root = &scan.Messages[i]
		case spec.Message:
			block = &scan.Messages[i]
		}
	}
	if root == nil {
		return ConfigBlockResult{}, ErrNoConfigRoot
	}
	res := ConfigBlockResult{File: root.File}

	// The block's full name as the scan qualifies a same-package reference.
	blockType := spec.Message
	if scan.Package != "" {
		blockType = scan.Package + "." + spec.Message
	}
	composed := false
	for _, f := range root.Fields {
		if f.Kind == "message" && f.TypeName == blockType {
			composed = true
			break
		}
		if f.Name == spec.Field {
			return res, fmt.Errorf("%s already has a field named %s (of type %s), so the %s block cannot be composed there; rename one of them",
				ConfigRootMessage, spec.Field, fieldTypeLabel(f), spec.Message)
		}
	}
	if block != nil {
		declared := make(map[string]string, len(block.Fields))
		for _, f := range block.Fields {
			if !f.Repeated && f.Oneof == "" {
				declared[f.Name] = f.Kind
			}
		}
		for _, leaf := range spec.Leaves {
			if declared[leaf.Name] != leaf.Type {
				return res, fmt.Errorf("%s already declares message %s without a %s field, which the scaffolded code reads; add `%s %s = <next tag> [(forge.v1.config) = {env_var: %q}];` to it, or remove the message",
					filepath.Base(block.File), spec.Message, leaf.Name, leaf.Type, leaf.Name, leaf.EnvVar)
			}
		}
	}
	if block != nil && composed {
		return res, nil
	}

	raw, err := os.ReadFile(root.File)
	if err != nil {
		return res, err
	}
	content := string(raw)
	if root.BodyClose <= 0 || root.BodyClose > len(content) || content[root.BodyClose] != '}' {
		return res, fmt.Errorf("could not locate the body of message %s in %s", ConfigRootMessage, filepath.Base(root.File))
	}
	if block == nil {
		if err := checkEnvVarsFree(scan, spec); err != nil {
			return res, err
		}
	}

	// Compose the block on the root first: it edits inside the root's body,
	// so the root's offsets stay valid until it is done. The message is then
	// appended to the end of the file, past every offset the scan recorded.
	if !composed {
		res.FieldNumber, err = nextConfigFieldNumber(content[root.BodyOpen:root.BodyClose])
		if err != nil {
			return res, fmt.Errorf("append %s.%s: %w", ConfigRootMessage, spec.Field, err)
		}
		head := strings.TrimRight(content[:root.BodyClose], " \t")
		if !strings.HasSuffix(head, "\n") {
			head += "\n" // a one-line body (`message AppConfig { int32 port = 1; }`)
		}
		field := "\n"
		if spec.FieldDoc != "" {
			field += "  // " + spec.FieldDoc + "\n"
		}
		field += fmt.Sprintf("  %s %s = %d;\n", spec.Message, spec.Field, res.FieldNumber)
		content = head + field + content[root.BodyClose:]
		res.AddedField = true
	}
	if block == nil {
		content = strings.TrimRight(content, "\n") + "\n\n" + renderConfigBlock(spec)
		res.AddedMessage = true
	}
	if res.AddedMessage {
		content = ensureConfigAnnotationImport(content)
	}
	if !write {
		return res, nil
	}
	if err := atomicfile.WriteFile(root.File, []byte(content), 0o644); err != nil { //nolint:gosec // a proto source file in the user's tree, as readable as the rest of it
		return res, err
	}
	return res, nil
}

// ensureConfigAnnotationImport inserts the annotation import after the syntax
// declaration without moving any of the author's message bytes.
func ensureConfigAnnotationImport(content string) string {
	if regexp.MustCompile(`(?m)^\s*import\s+(?:public\s+)?"forge/v1/forge.proto"`).MatchString(content) {
		return content
	}
	syntax := regexp.MustCompile(`\bsyntax\s*=\s*"proto[23]"\s*;`).FindStringIndex(content)
	if syntax == nil {
		return "import \"forge/v1/forge.proto\";\n" + content
	}
	return content[:syntax[1]] + "\n\nimport \"forge/v1/forge.proto\";" + content[syntax[1]:]
}

// nextConfigFieldNumber reads the root's number space, including oneofs and
// reserved ranges, but excluding nested message/enum number spaces. Tokenizing
// skips comments and strings; scanning every '=' also handles multiple fields
// on one line, which the birth scanner's high-water mark cannot always see.
func nextConfigFieldNumber(body string) (int, error) {
	var s scanner.Scanner
	s.Init(strings.NewReader(body))
	s.Mode = scanner.ScanIdents | scanner.ScanInts | scanner.ScanStrings | scanner.ScanComments | scanner.SkipComments
	depth, ignoredDepth, high := 0, -1, 0
	for tok := s.Scan(); tok != scanner.EOF; tok = s.Scan() {
		text := s.TokenText()
		if ignoredDepth >= 0 {
			if tok == '{' {
				depth++
			} else if tok == '}' {
				depth--
				if depth == ignoredDepth {
					ignoredDepth = -1
				}
			}
			continue
		}
		if tok == scanner.Ident && (text == "message" || text == "enum") {
			for tok = s.Scan(); tok != scanner.EOF && tok != '{'; tok = s.Scan() {
			}
			ignoredDepth = depth
			depth++
			continue
		}
		if tok == '{' {
			depth++
		} else if tok == '}' {
			depth--
		}
		if tok == scanner.Ident && text == "reserved" {
			for tok = s.Scan(); tok != scanner.EOF && tok != ';'; tok = s.Scan() {
				if tok == scanner.Ident && s.TokenText() == "max" {
					return 0, fmt.Errorf("reserved range extends to max; choose a free tag explicitly")
				}
				if tok == scanner.Int {
					n, _ := strconv.Atoi(s.TokenText())
					if n > high {
						high = n
					}
				}
			}
		}
		if tok == '=' && s.Scan() == scanner.Int {
			n, _ := strconv.Atoi(s.TokenText())
			if n > high {
				high = n
			}
		}
	}
	next := high + 1
	if next >= 19000 && next <= 19999 {
		next = 20000 // protobuf's implementation-reserved range
	}
	if next > 536870911 {
		return 0, fmt.Errorf("no tag above the existing high-water mark is available")
	}
	return next, nil
}

// renderConfigBlock renders spec as a proto message declaration.
func renderConfigBlock(spec ConfigBlockSpec) string {
	var b strings.Builder
	for _, line := range spec.Doc {
		if line == "" {
			b.WriteString("//\n")
			continue
		}
		b.WriteString("// " + line + "\n")
	}
	fmt.Fprintf(&b, "message %s {\n", spec.Message)
	for i, leaf := range spec.Leaves {
		fmt.Fprintf(&b, "  %s %s = %d [(forge.v1.config) = {\n", leaf.Type, leaf.Name, i+1)
		fmt.Fprintf(&b, "    env_var: %q\n", leaf.EnvVar)
		fmt.Fprintf(&b, "    description: %q\n", leaf.Description)
		b.WriteString("  }];\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// envVarRE matches one (forge.v1.config) env_var binding.
var envVarRE = regexp.MustCompile(`env_var:\s*"([^"]*)"`)

// checkEnvVarsFree refuses a new block whose leaf env var some field in the
// package already binds: two fields on one variable is a config generate
// rejects, and a scaffold should not be what writes it.
func checkEnvVarsFree(scan *codegen.RawProtoScan, spec ConfigBlockSpec) error {
	bound := map[string]string{}
	for _, f := range scan.Files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, m := range envVarRE.FindAllStringSubmatch(string(raw), -1) {
			bound[m[1]] = filepath.Base(f)
		}
	}
	for _, leaf := range spec.Leaves {
		if file, taken := bound[leaf.EnvVar]; taken {
			return fmt.Errorf("env var %s is already bound by a field in %s; the %s block's %s leaf cannot take it too",
				leaf.EnvVar, file, spec.Message, leaf.Name)
		}
	}
	return nil
}

// fieldTypeLabel names a scanned field's type for an error message.
func fieldTypeLabel(f codegen.SchemaFieldDef) string {
	if f.TypeName != "" {
		return f.TypeName
	}
	return f.Kind
}
