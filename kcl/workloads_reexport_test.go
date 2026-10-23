package kcl

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestWorkloadsReExportsEveryTiersTypeWorkloadReaches: a project that imports
// `forge.workloads as fw` must be able to NAME every type an fw.Workload field
// is typed with — fw.Workload.securityContext is a tiers.PodSecurity, so a
// missing `fw.PodSecurity` forces a second `import forge.tiers` for one type.
//
// The reachable set is walked, not listed: start from the tiers types
// forge.Workload's fields mention (kcl/workload.k), follow every field of each
// generated tiers schema (kcl/tiers/tiers_gen.k) to the tiers types IT
// mentions, and require each one as `<Name> = tiers.<Name>` in
// kcl/workloads/schema.k. A new field on either side is covered automatically.
func TestWorkloadsReExportsEveryTiersTypeWorkloadReaches(t *testing.T) {
	workload := readFile(t, "workload.k")
	tiersGen := readFile(t, "tiers/tiers_gen.k")
	reexports := readFile(t, "workloads/schema.k")

	tiersSchemas := schemaBodies(tiersGen)
	if len(tiersSchemas) == 0 {
		t.Fatal("no schemas parsed from tiers/tiers_gen.k")
	}

	body, ok := schemaBodies(workload)["Workload"]
	if !ok {
		t.Fatal("schema Workload not found in workload.k")
	}
	var queue []string
	for _, m := range regexp.MustCompile(`tiers\.([A-Z]\w*)`).FindAllStringSubmatch(fieldLines(body), -1) {
		queue = append(queue, m[1])
	}
	if len(queue) == 0 {
		t.Fatal("forge.Workload mentions no tiers type: the parse is broken")
	}
	reached := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if reached[name] {
			continue
		}
		reached[name] = true
		sub, ok := tiersSchemas[name]
		if !ok {
			t.Errorf("forge.Workload reaches tiers.%s, which tiers_gen.k does not define", name)
			continue
		}
		// Inside tiers_gen.k field types name sibling schemas unqualified.
		for _, m := range regexp.MustCompile(`\b([A-Z]\w*)\b`).FindAllStringSubmatch(fieldLines(sub), -1) {
			if _, isSchema := tiersSchemas[m[1]]; isSchema {
				queue = append(queue, m[1])
			}
		}
	}

	var missing []string
	for name := range reached {
		if !regexp.MustCompile(`(?m)^` + name + `\s*=\s*tiers\.` + name + `\s*$`).MatchString(reexports) {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("forge.workloads does not re-export tiers types forge.Workload's fields reach: %v\n"+
			"add `<Name> = tiers.<Name>` for each to kcl/workloads/schema.k", missing)
	}
}

func readFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// schemaBodies maps each top-level `schema X:` to its indented body.
func schemaBodies(src string) map[string]string {
	out := map[string]string{}
	header := regexp.MustCompile(`^schema (\w+)(\(\w+\))?:`)
	var name string
	var body []string
	flush := func() {
		if name != "" {
			out[name] = strings.Join(body, "\n")
		}
	}
	for _, line := range strings.Split(src, "\n") {
		if m := header.FindStringSubmatch(line); m != nil {
			flush()
			name, body = m[1], nil
			continue
		}
		if name == "" {
			continue
		}
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			flush()
			name, body = "", nil
			continue
		}
		body = append(body, line)
	}
	flush()
	return out
}

// fieldLines keeps only a schema body's attribute declarations
// (`    name?: Type = default`), dropping docstrings, comments and checks.
func fieldLines(body string) string {
	attr := regexp.MustCompile(`^    \w+\??\s*:\s*(.*)$`)
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "check:") {
			break
		}
		if m := attr.FindStringSubmatch(line); m != nil {
			typ := m[1]
			if i := strings.Index(typ, " = "); i >= 0 {
				typ = typ[:i]
			}
			out = append(out, typ)
		}
	}
	return strings.Join(out, "\n")
}
