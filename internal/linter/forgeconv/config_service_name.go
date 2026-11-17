// File: internal/linter/forgeconv/config_service_name.go
//
// forgeconv-config-service-name — a config field must not set
// OTEL_SERVICE_NAME.
//
// service.name is a per-WORKLOAD fact: it is how every span, metric and
// error report is attributed to the process that produced it, and one
// binary commonly runs as several workloads (an api, a worker, an operator).
// forge.render sets OTEL_SERVICE_NAME to each workload's own name unless that
// workload's env sets one.
//
// A config field projects ONE value into every workload's env, so a config
// field bound to OTEL_SERVICE_NAME overrides the per-workload name with a
// project-wide one. forge's scaffold shipped exactly that field until
// 0d94e103 (`string service_name = 22`, default "unknown"), and nothing read
// it: its only effect was the projection. In control-plane's prod it filed
// reliant-api-server, reliant-temporal-worker and daemon-gateway all as
// service "unknown".
//
// Every project scaffolded before that change still carries the field, so
// the rule is the migration: delete it and reserve its tag and name. A
// workload that really wants a different name sets OTEL_SERVICE_NAME in its
// own env. Severity is ERROR; the usual
// `// forge:lint-disable-next-line forgeconv-config-service-name: <why>`
// above the field is the escape hatch.

package forgeconv

import (
	"fmt"
	"regexp"
	"strings"
)

const ruleConfigServiceName = "forgeconv-config-service-name"

var (
	// configServiceNameEnvRE matches the annotation line binding the field
	// to OTEL_SERVICE_NAME.
	configServiceNameEnvRE = regexp.MustCompile(`\benv_var\s*:\s*"OTEL_SERVICE_NAME"`)
	// protoFieldDeclRE matches a field declaration's opening line:
	// `<type> <name> = <tag>`.
	protoFieldDeclRE = regexp.MustCompile(`^\s*(?:optional\s+)?[\w.]+\s+(\w+)\s*=\s*\d+`)
)

// checkConfigServiceName reports every field whose (forge.v1.config)
// annotation names OTEL_SERVICE_NAME, at the field's declaration line.
func checkConfigServiceName(relPath, content string) []Finding {
	if !strings.Contains(content, "OTEL_SERVICE_NAME") {
		return nil
	}
	lines := strings.Split(content, "\n")
	var findings []Finding
	for i, line := range lines {
		if !configServiceNameEnvRE.MatchString(stripLineComment(line)) {
			continue
		}
		// The annotation sits inside the field's options; the field is
		// the nearest declaration at or above it.
		declLine, name := i, ""
		for j := i; j >= 0 && j >= i-10; j-- {
			if m := protoFieldDeclRE.FindStringSubmatch(stripLineComment(lines[j])); m != nil {
				declLine, name = j, m[1]
				break
			}
		}
		field := "this field"
		if name != "" {
			field = fmt.Sprintf("config field %q", name)
		}
		findings = append(findings, Finding{
			Rule:     ruleConfigServiceName,
			Severity: SeverityError,
			File:     relPath,
			Line:     declLine + 1,
			Message: fmt.Sprintf("%s sets OTEL_SERVICE_NAME, so every workload's env gets the same service.name and "+
				"none is attributed to its own process; forge.render already gives each workload its own name", field),
			Remediation: "delete the field and reserve its tag and name (`reserved <tag>; reserved \"<name>\";`). " +
				"A workload that needs a different service.name sets OTEL_SERVICE_NAME in its own env.",
		})
	}
	return findings
}

// stripLineComment drops a trailing `//` comment, so a commented-out field
// is not reported.
func stripLineComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}
