package siteaudit

import (
	"net/url"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Head is what a page's head says. [ExtractHead] fills the fields read from
// the HTML; [Evaluate] also fills XRobotsTag, Indexable, OGComplete and
// ClientRendered, and reads Social from the raw HTML and the rest from the
// DOM it judged (D1).
type Head struct {
	// Title is the document title, whitespace collapsed.
	Title string `json:"title,omitempty"`
	// Description is the meta description.
	Description string `json:"description,omitempty"`
	// Canonical is the first <link rel="canonical"> href, as written.
	Canonical string `json:"canonical,omitempty"`
	// Lang is <html lang>.
	Lang string `json:"lang,omitempty"`
	// Robots is the meta robots (and googlebot) content, comma-joined.
	Robots string `json:"robots,omitempty"`
	// Viewport is the meta viewport content.
	Viewport string `json:"viewport,omitempty"`
	// Social is the first value of each og:* and twitter:* meta tag.
	Social map[string]string `json:"social,omitempty"`
	// Hreflang is the <link rel="alternate" hreflang> set.
	Hreflang []Alternate `json:"hreflang,omitempty"`
	// JSONLDTypes are the @type values of the page's JSON-LD, sorted.
	JSONLDTypes []string `json:"jsonld_types,omitempty"`

	// XRobotsTag is the X-Robots-Tag response header (set by Evaluate).
	XRobotsTag string `json:"x_robots_tag,omitempty"`
	// Indexable reports that nothing keeps the page out of search: no
	// noindex, and robots.txt allows it (set by Evaluate).
	Indexable bool `json:"indexable"`
	// OGComplete reports og:title, og:type, og:image and og:url in the raw
	// HTML (set by Evaluate).
	OGComplete bool `json:"og_complete"`
	// ClientRendered names the tags (title, description, canonical) only the
	// rendered DOM has (set by Evaluate).
	ClientRendered []string `json:"client_rendered,omitempty"`
}

// Alternate is one hreflang alternate.
type Alternate struct {
	Hreflang string `json:"hreflang"`
	Href     string `json:"href"`
}

// ExtractHead reads a page's head facts from its HTML. It never fails: a
// document the parser cannot make sense of yields an empty Head.
func ExtractHead(src string) Head {
	return parseDocument(src).head()
}

// DiscoverLinks returns the absolute http(s) URLs a page's anchors point at,
// resolved against pageURL and the document's <base href>, fragments
// removed, deduplicated, in document order. It is what a crawler follows.
func DiscoverLinks(pageURL, src string) []string {
	d := parseDocument(src)
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	base = d.resolveBase(base)
	seen := map[string]bool{}
	var out []string
	for _, a := range d.anchors {
		if !a.hasHref {
			continue
		}
		u := resolveHTTP(base, a.href)
		if u == nil {
			continue
		}
		s := u.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// document is one parsed HTML page.
type document struct {
	lang         string
	titles       []string
	descriptions []string
	canonicals   []string
	robots       []string
	viewport     string
	hasViewport  bool
	base         string
	social       map[string]string
	alternates   []Alternate
	jsonld       []string
	anchors      []anchor
	images       []image
}

type anchor struct {
	href     string
	hasHref  bool
	text     string // accessible text: content, img alt, aria-label, title
	named    bool   // a jump target (<a name> / <a id>) with no href
	onclick  bool
	ariaHide bool
}

type image struct {
	src    string
	hasAlt bool
	hidden bool // role=presentation|none or aria-hidden=true
}

func parseDocument(src string) *document {
	d := &document{social: map[string]string{}}
	root, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return d
	}
	sawHTML := false
	var walk func(n *html.Node, inHead bool)
	walk = func(n *html.Node, inHead bool) {
		if n.Type == html.ElementNode && n.Namespace == "" {
			switch n.DataAtom {
			case atom.Html:
				if !sawHTML {
					sawHTML = true
					d.lang = strings.TrimSpace(attr(n, "lang"))
				}
			case atom.Head:
				inHead = true
			case atom.Title:
				if inHead {
					d.titles = append(d.titles, collapse(textContent(n)))
				}
			case atom.Meta:
				d.meta(n, inHead)
			case atom.Link:
				d.link(n, inHead)
			case atom.Base:
				if d.base == "" && hasAttr(n, "href") {
					d.base = strings.TrimSpace(attr(n, "href"))
				}
			case atom.Script:
				if strings.EqualFold(strings.TrimSpace(attr(n, "type")), "application/ld+json") {
					d.jsonld = append(d.jsonld, textContent(n))
				}
				return
			case atom.Style, atom.Template:
				return
			case atom.A:
				d.anchors = append(d.anchors, readAnchor(n))
			case atom.Img:
				role := strings.ToLower(strings.TrimSpace(attr(n, "role")))
				d.images = append(d.images, image{
					src:    strings.TrimSpace(attr(n, "src")),
					hasAlt: hasAttr(n, "alt"),
					hidden: role == "presentation" || role == "none" || strings.EqualFold(attr(n, "aria-hidden"), "true"),
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, inHead)
		}
	}
	walk(root, false)
	return d
}

func (d *document) meta(n *html.Node, inHead bool) {
	content := strings.TrimSpace(attr(n, "content"))
	name := strings.ToLower(strings.TrimSpace(attr(n, "name")))
	prop := strings.ToLower(strings.TrimSpace(attr(n, "property")))
	switch {
	case name == "description" && inHead:
		d.descriptions = append(d.descriptions, collapse(content))
	case (name == "robots" || name == "googlebot") && inHead:
		d.robots = append(d.robots, content)
	case name == "viewport" && inHead:
		if !d.hasViewport {
			d.hasViewport, d.viewport = true, content
		}
	}
	for _, key := range []string{prop, name} {
		if strings.HasPrefix(key, "og:") || strings.HasPrefix(key, "twitter:") {
			if _, ok := d.social[key]; !ok {
				d.social[key] = content
			}
		}
	}
}

func (d *document) link(n *html.Node, inHead bool) {
	rels := strings.Fields(strings.ToLower(attr(n, "rel")))
	href := strings.TrimSpace(attr(n, "href"))
	for _, rel := range rels {
		switch rel {
		case "canonical":
			if inHead {
				d.canonicals = append(d.canonicals, href)
			}
		case "alternate":
			if hasAttr(n, "hreflang") {
				d.alternates = append(d.alternates, Alternate{Hreflang: strings.TrimSpace(attr(n, "hreflang")), Href: href})
			}
		}
	}
}

func readAnchor(n *html.Node) anchor {
	a := anchor{
		hasHref:  hasAttr(n, "href"),
		href:     strings.TrimSpace(attr(n, "href")),
		onclick:  hasAttr(n, "onclick"),
		ariaHide: strings.EqualFold(attr(n, "aria-hidden"), "true"),
	}
	a.named = !a.hasHref && (hasAttr(n, "name") || hasAttr(n, "id"))
	parts := []string{textContent(n)}
	var alts func(*html.Node)
	alts = func(c *html.Node) {
		for ; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				if c.DataAtom == atom.Img {
					parts = append(parts, attr(c, "alt"))
				}
				if l := attr(c, "aria-label"); l != "" {
					parts = append(parts, l)
				}
				alts(c.FirstChild)
			}
		}
	}
	alts(n.FirstChild)
	parts = append(parts, attr(n, "aria-label"), attr(n, "title"))
	a.text = collapse(strings.Join(parts, " "))
	return a
}

func (d *document) head() Head {
	h := Head{
		Lang:     d.lang,
		Viewport: d.viewport,
		Robots:   strings.Join(d.robots, ", "),
		Hreflang: d.alternates,
	}
	if len(d.titles) > 0 {
		h.Title = d.titles[0]
	}
	if len(d.descriptions) > 0 {
		h.Description = d.descriptions[0]
	}
	if len(d.canonicals) > 0 {
		h.Canonical = d.canonicals[0]
	}
	if len(d.social) > 0 {
		h.Social = make(map[string]string, len(d.social))
		for k, v := range d.social {
			h.Social[k] = v
		}
	}
	seen := map[string]bool{}
	for _, raw := range d.jsonld {
		for _, t := range jsonldTypes(raw) {
			if !seen[t] {
				seen[t] = true
				h.JSONLDTypes = append(h.JSONLDTypes, t)
			}
		}
	}
	sort.Strings(h.JSONLDTypes)
	return h
}

// resolveBase applies the document's <base href> to the page URL.
func (d *document) resolveBase(page *url.URL) *url.URL {
	if d.base == "" {
		return page
	}
	b, err := page.Parse(d.base)
	if err != nil {
		return page
	}
	return b
}

// resolveHTTP resolves href against base and returns it when it is an
// http(s) URL, fragment removed; nil otherwise.
func resolveHTTP(base *url.URL, href string) *url.URL {
	u, err := base.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	u.Fragment, u.RawFragment = "", ""
	return u
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

// textContent is the text under n, skipping scripts, styles and templates.
func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		for ; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				b.WriteString(c.Data)
			case html.ElementNode:
				if c.DataAtom == atom.Style || c.DataAtom == atom.Template || (c.DataAtom == atom.Script && n.DataAtom != atom.Script) {
					continue
				}
				walk(c.FirstChild)
			}
		}
	}
	walk(n.FirstChild)
	return b.String()
}

// collapse trims s and collapses runs of whitespace to one space.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
