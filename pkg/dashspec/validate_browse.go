package dashspec

import (
	"slices"
	"strconv"
	"strings"
)

// link validates Panel.Link on a panel of kind: only a panel with rows takes
// one, and it names its target table and either a filter or a row key.
func (v *validator) link(path string, o map[string]any, kind PanelKind, raw any) {
	lp := childPath(path, "link")
	byGroup := false // a FromField is one of the panel's groupBy fields
	switch kind {
	case KindTable, KindBar:
		byGroup = true
	case KindPostgres:
		if q, ok := o["postgres"].(map[string]any); ok {
			if d, _ := q["draw"].(string); d != "" && d != PostgresDrawAuto && d != PostgresDrawTable && d != PostgresDrawBar {
				v.add(lp, "a link opens the row it is clicked on, and a postgres panel drawn as %q has no rows to click: draw it as %s, %s or %s", d, PostgresDrawTable, PostgresDrawBar, PostgresDrawAuto)
			}
		}
	default:
		v.add(lp, "only a table, bar or postgres panel takes \"link\": a link opens the row it is clicked on, and a %s panel draws none", kind)
		return
	}
	l, ok := v.object(lp, raw)
	if !ok {
		return
	}
	v.known(lp, l, "database", "table", "column", "fromField", "row", "fromFields")
	v.databaseName(lp, l)
	v.tableName(lp, l)

	groups := stringsOf(o["groupBy"])
	field := func(p, f string) {
		switch {
		case !byGroup:
			v.columnName(p, f)
		case len(groups) == 0:
			v.add(p, "a %s panel with no \"groupBy\" draws one row of totals, which carries no %q to link by: group it by the field to link on", kind, f)
		case !slices.Contains(groups, f):
			v.add(p, "%q is not one of the panel's groupBy fields (%s): a %s panel's rows carry only those", f, strings.Join(groups, ", "), kind)
		}
	}

	row := false
	if r, has := l["row"]; has {
		b, isBool := r.(bool)
		if !isBool {
			v.add(childPath(lp, "row"), "must be true or false")
		}
		row = b
	}
	_, hasColumn := l["column"]
	_, hasFrom := l["fromField"]
	_, hasFroms := l["fromFields"]
	if !row {
		if hasFroms {
			v.add(childPath(lp, "fromFields"), "only a row link (\"row\": true) takes \"fromFields\"; a filter link reads its one value from \"fromField\"")
		}
		if !hasColumn && !hasFrom {
			if !hasFroms {
				v.add(lp, "a link names either \"column\" and \"fromField\" (open the table filtered by the clicked row's value) or \"row\": true and \"fromFields\" (open one row by its key)")
			}
			return
		}
		if c := v.str(lp, l, "column", true); c != "" {
			v.columnName(childPath(lp, "column"), c)
		}
		if f := v.str(lp, l, "fromField", true); f != "" {
			field(childPath(lp, "fromField"), f)
		}
		return
	}
	for _, k := range []string{"column", "fromField"} {
		if _, has := l[k]; has {
			v.add(childPath(lp, k), "a row link opens one row by its key and takes no %q (a filter link is \"column\" and \"fromField\" with no \"row\")", k)
		}
	}
	keys, ok := v.list(lp, l, "fromFields")
	if !ok {
		if !hasFroms {
			v.add(lp, "a row link needs \"fromFields\": the fields holding the row's primary key, in key order")
		}
		return
	}
	fp := childPath(lp, "fromFields")
	if len(keys) == 0 || len(keys) > MaxLinkKeyFields {
		v.add(fp, "a row link names 1 to %d key fields, has %d", MaxLinkKeyFields, len(keys))
	}
	seen := map[string]bool{}
	for i, raw := range keys {
		p := indexPath(fp, i)
		s, ok := raw.(string)
		if !ok {
			v.add(p, "must be a string")
			continue
		}
		if seen[s] {
			v.add(p, "%q is named twice; a key reads each field once", s)
		}
		seen[s] = true
		field(p, s)
	}
}

// browsePanel validates a KindBrowse panel: its browse block is all it
// carries, besides the common presentation fields.
func (v *validator) browsePanel(path string, o map[string]any) {
	v.refuse(path, o, KindBrowse, "signal", "series", "sql", "ratio", "formula", "limit", "where", "groupBy", "columns", "funnel", "hasError", "postgres", "unit")
	raw, present := o["browse"]
	if !present {
		v.add(path, "a browse panel needs a \"browse\" block: {\"database\", \"table\"}")
		return
	}
	bp := childPath(path, "browse")
	b, ok := v.object(bp, raw)
	if !ok {
		return
	}
	v.known(bp, b, "database", "table", "columns", "filters", "sort", "size")
	v.databaseName(bp, b)
	v.tableName(bp, b)

	if cols, ok := v.list(bp, b, "columns"); ok {
		cp := childPath(bp, "columns")
		if len(cols) > MaxBrowseColumns {
			v.add(cp, "shows %d columns, over the %d allowed (leave \"columns\" out to show every column)", len(cols), MaxBrowseColumns)
		}
		seen := map[string]bool{}
		for i, raw := range cols {
			p := indexPath(cp, i)
			s, ok := raw.(string)
			if !ok {
				v.add(p, "must be a string")
				continue
			}
			v.columnName(p, s)
			if seen[s] {
				v.add(p, "column %q is shown twice", s)
			}
			seen[s] = true
		}
	}

	if fs, ok := v.list(bp, b, "filters"); ok {
		if len(fs) > MaxBrowseFilters {
			v.add(childPath(bp, "filters"), "has %d filters, over the %d allowed", len(fs), MaxBrowseFilters)
		}
		for i, raw := range fs {
			p := indexPath(childPath(bp, "filters"), i)
			if f, ok := v.object(p, raw); ok {
				v.browseFilter(p, f)
			}
		}
	}

	if ss, ok := v.list(bp, b, "sort"); ok {
		if len(ss) > MaxBrowseSort {
			v.add(childPath(bp, "sort"), "has %d sort keys, over the %d allowed", len(ss), MaxBrowseSort)
		}
		seen := map[string]bool{}
		for i, raw := range ss {
			p := indexPath(childPath(bp, "sort"), i)
			s, ok := v.object(p, raw)
			if !ok {
				continue
			}
			v.known(p, s, "column", "desc")
			if c := v.str(p, s, "column", true); c != "" {
				v.columnName(childPath(p, "column"), c)
				if seen[c] {
					v.add(childPath(p, "column"), "column %q is sorted on twice", c)
				}
				seen[c] = true
			}
			if d, has := s["desc"]; has {
				if _, isBool := d.(bool); !isBool {
					v.add(childPath(p, "desc"), "must be true or false")
				}
			}
		}
	}

	if n, has := v.number(bp, b, "size"); has && (n != float64(int(n)) || !slices.Contains(BrowseSizes(), int(n))) {
		sizes := make([]string, 0, len(BrowseSizes()))
		for _, s := range BrowseSizes() {
			sizes = append(sizes, strconv.Itoa(s))
		}
		v.add(childPath(bp, "size"), "is not a page size: one of %s (leave it out for %d)", strings.Join(sizes, ", "), DefaultBrowseSize)
	}
}

// browseFilter validates one BrowseFilter: a column, an operator of the data
// browser's, the values the operator takes, and parameter references that name
// a parameter the dashboard has.
func (v *validator) browseFilter(path string, f map[string]any) {
	v.known(path, f, "column", "op", "values")
	if c := v.str(path, f, "column", true); c != "" {
		v.columnName(childPath(path, "column"), c)
	}
	ops := make([]string, 0, len(BrowseOps()))
	for _, op := range BrowseOps() {
		ops = append(ops, string(op))
	}
	op := BrowseOp(v.oneOf(path, f, "op", true, ops...))

	vs, listed := v.list(path, f, "values")
	vp := childPath(path, "values")
	for i, raw := range vs {
		p := indexPath(vp, i)
		s, ok := raw.(string)
		if !ok {
			v.add(p, "must be a string")
			continue
		}
		if len(s) > MaxBrowseValueBytes {
			v.add(p, "is %d bytes, over the %d a filter value may hold", len(s), MaxBrowseValueBytes)
		}
		if name, ok := BrowseParam(s); ok && name != BrowseParamFrom && name != BrowseParamTo && !v.filterIDs[name] {
			v.add(p, "%s references no parameter: a value may reference {%s}, {%s} or the id of a dashboard filter%s",
				s, BrowseParamFrom, BrowseParamTo, v.declaredFilters())
		}
	}
	if _, present := f["values"]; present && !listed {
		return // not an array, already reported
	}
	n, known := BrowseOpArity(op)
	switch {
	case !known:
	case n == 0 && len(vs) > 0:
		v.add(vp, "%q takes no values, has %d", op, len(vs))
	case n == 1 && len(vs) != 1:
		v.add(path, "%q takes exactly 1 value in \"values\", has %d", op, len(vs))
	case n < 0 && (len(vs) == 0 || len(vs) > MaxBrowseInValues):
		v.add(path, "%q takes 1 to %d values in \"values\", has %d", op, MaxBrowseInValues, len(vs))
	}
}

// declaredFilters names the dashboard's filter ids for a problem sentence.
func (v *validator) declaredFilters() string {
	if len(v.filterIDs) == 0 {
		return " (this dashboard declares none)"
	}
	ids := make([]string, 0, len(v.filterIDs))
	for id := range v.filterIDs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return " (" + strings.Join(ids, ", ") + ")"
}

// tableName validates obj's required "table": schema-qualified, as the data
// browser names a table.
func (v *validator) tableName(path string, obj map[string]any) {
	t := v.str(path, obj, "table", true)
	if t == "" {
		return
	}
	schema, name, qualified := strings.Cut(t, ".")
	switch {
	case !qualified:
		v.add(childPath(path, "table"), "%q is not schema-qualified: name it as <schema>.<table>, like \"public.%s\"", t, t)
	case schema == "" || name == "" || len(t) > MaxTableNameLen:
		v.add(childPath(path, "table"), "%q is not a table name: <schema>.<table>, each part non-empty, at most %d bytes in all", t, MaxTableNameLen)
	}
}

// stringsOf is the strings of a JSON array, skipping anything else.
func stringsOf(raw any) []string {
	l, _ := raw.([]any)
	var out []string
	for _, e := range l {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
