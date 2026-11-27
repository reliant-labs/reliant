package siteaudit

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// jsonldRequired are the properties a rich result needs, per schema.org
// type, following Google's structured-data documentation. A type not listed
// is not checked. Product's one-of rule and the nested ListItem / Question
// checks are in checkJSONLDNode.
var jsonldRequired = map[string][]string{
	"WebSite":        {"name", "url"},
	"Organization":   {"name", "url"},
	"Article":        {"headline"},
	"NewsArticle":    {"headline"},
	"BlogPosting":    {"headline"},
	"Product":        {"name"},
	"BreadcrumbList": {"itemListElement"},
	"FAQPage":        {"mainEntity"},
}

// jsonldTypes returns every top-level (and @graph) @type in one JSON-LD
// block; nothing for a block that does not parse.
func jsonldTypes(raw string) []string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return nil
	}
	var out []string
	for _, n := range jsonldNodes(v) {
		out = append(out, typesOf(n)...)
	}
	return out
}

// jsonldTopLevel returns the block's top-level objects (an array's items).
func jsonldTopLevel(v any) []map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return []map[string]any{t}
	case []any:
		var out []map[string]any
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// jsonldNodes returns the block's entity nodes: the top-level objects and
// the members of any @graph.
func jsonldNodes(v any) []map[string]any {
	var out []map[string]any
	for _, m := range jsonldTopLevel(v) {
		if g, ok := m["@graph"]; ok {
			out = append(out, jsonldTopLevel(g)...)
			if _, typed := m["@type"]; !typed {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func typesOf(n map[string]any) []string {
	switch t := n["@type"].(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// present reports a property with a non-empty value.
func present(n map[string]any, prop string) bool {
	v, ok := n[prop]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// jsonldProblem is one problem in one block.
type jsonldProblem struct {
	code string // jsonld_invalid, jsonld_no_context, jsonld_missing_property
	ref  string
	msg  string
}

// checkJSONLD judges one block (index i on the page).
func checkJSONLD(i int, raw string) []jsonldProblem {
	block := fmt.Sprintf("jsonld[%d]", i)
	var v any
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err != nil {
		return []jsonldProblem{{code: "jsonld_invalid", ref: block, msg: err.Error()}}
	}
	top := jsonldTopLevel(v)
	if len(top) == 0 {
		return []jsonldProblem{{code: "jsonld_invalid", ref: block, msg: "the block is not a JSON object or array of objects"}}
	}
	var out []jsonldProblem
	for _, m := range top {
		if !present(m, "@context") {
			out = append(out, jsonldProblem{code: "jsonld_no_context", ref: block, msg: "no @context"})
			break
		}
	}
	for _, n := range jsonldNodes(v) {
		out = append(out, checkJSONLDNode(block, n)...)
	}
	return out
}

func checkJSONLDNode(block string, n map[string]any) []jsonldProblem {
	var out []jsonldProblem
	missing := func(typ, prop string) {
		out = append(out, jsonldProblem{code: "jsonld_missing_property", ref: block + " " + typ + "." + prop,
			msg: fmt.Sprintf("%s needs %s", typ, prop)})
	}
	types := typesOf(n)
	sort.Strings(types)
	for _, typ := range types {
		for _, prop := range jsonldRequired[typ] {
			if !present(n, prop) {
				missing(typ, prop)
			}
		}
		switch typ {
		case "Product":
			if !present(n, "offers") && !present(n, "review") && !present(n, "aggregateRating") {
				missing(typ, "offers|review|aggregateRating")
			}
		case "BreadcrumbList":
			items := objects(n["itemListElement"])
			for i, it := range items {
				p := fmt.Sprintf("itemListElement[%d]", i)
				if !present(it, "position") {
					missing(typ, p+".position")
				}
				item, _ := it["item"].(map[string]any)
				if !present(it, "name") && (item == nil || !present(item, "name")) {
					missing(typ, p+".name")
				}
				// The last crumb may omit item: it is the page itself.
				if i < len(items)-1 && !present(it, "item") {
					missing(typ, p+".item")
				}
			}
		case "FAQPage":
			for i, q := range objects(n["mainEntity"]) {
				p := fmt.Sprintf("mainEntity[%d]", i)
				if !present(q, "name") {
					missing(typ, p+".name")
				}
				a, _ := q["acceptedAnswer"].(map[string]any)
				if a == nil || !present(a, "text") {
					missing(typ, p+".acceptedAnswer.text")
				}
			}
		}
	}
	return out
}

// objects returns v as a list of objects: one object, or an array's objects.
func objects(v any) []map[string]any {
	return jsonldTopLevel(v)
}
