package dashspec

// sectionTargets validates cross-references only after the panel inventory is
// complete, so filters can target panels declared later in the document.
func (v *validator) sectionTargets(d map[string]any, panels []any, panelIds map[string]bool) {
	sections := map[string]bool{}
	if list, ok := v.list("", d, "sections"); ok {
		if len(list) > maxPanelsPerDashboard {
			v.add("sections", "has more than %d sections", maxPanelsPerDashboard)
		}
		for i, raw := range list {
			path := indexPath("sections", i)
			o, ok := v.object(path, raw)
			if !ok {
				continue
			}
			v.known(path, o, "id", "title")
			id := v.str(path, o, "id", true)
			v.str(path, o, "title", true)
			if id == "" {
				continue
			}
			if !idPattern.MatchString(id) {
				v.add(childPath(path, "id"), "invalid section id %q", id)
			}
			if sections[id] {
				v.add(childPath(path, "id"), "duplicate section id %q", id)
			}
			sections[id] = true
		}
	}
	for i, raw := range panels {
		o, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path := indexPath("panels", i)
		if id := v.str(path, o, "section", false); id != "" && !sections[id] {
			v.add(childPath(path, "section"), "undeclared section %q", id)
		}
	}
	if list, ok := d["filters"].([]any); ok {
		for i, raw := range list {
			o, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			path := indexPath("filters", i)
			targets, ok := v.list(path, o, "panelIds")
			if !ok {
				continue
			}
			if len(targets) > maxPanelsPerDashboard {
				v.add(childPath(path, "panelIds"), "has more than %d targets", maxPanelsPerDashboard)
			}
			seen := map[string]bool{}
			for j, raw := range targets {
				p := indexPath(childPath(path, "panelIds"), j)
				id, ok := raw.(string)
				if !ok {
					v.add(p, "must be a panel id string")
					continue
				}
				if !panelIds[id] {
					v.add(p, "undeclared panel %q", id)
				}
				if seen[id] {
					v.add(p, "duplicate panel target %q", id)
				}
				seen[id] = true
			}
		}
	}
}
