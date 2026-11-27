package siteaudit

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The sitemaps protocol's limits per file (sitemaps.org): 50,000 URLs and
// 50 MB (52,428,800 bytes) uncompressed.
const (
	SitemapMaxURLs  = 50000
	SitemapMaxBytes = 50 * 1024 * 1024
)

// Sitemap is a parsed sitemap file: a urlset or a sitemap index.
type Sitemap struct {
	// Index reports a <sitemapindex>; its Locs are child sitemaps.
	Index bool
	// Locs are the <loc> values, trimmed, in file order.
	Locs []string
	// Bytes is the body's size.
	Bytes int
}

// ErrNotSitemap is returned for a body that is not a sitemap.
var ErrNotSitemap = errors.New("not a sitemap: the root element must be <urlset> or <sitemapindex>")

// ParseSitemap parses an uncompressed sitemap body. It returns an error for
// a body that is not well-formed XML or whose root is neither <urlset> nor
// <sitemapindex>. It reads every <loc>, so the caller judges the limits.
func ParseSitemap(body string) (*Sitemap, error) {
	dec := xml.NewDecoder(strings.NewReader(body))
	dec.Strict = true
	s := &Sitemap{Bytes: len(body)}
	depth := 0
	inLoc := false
	var loc strings.Builder
	rooted := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("sitemap XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				switch t.Name.Local {
				case "urlset":
				case "sitemapindex":
					s.Index = true
				default:
					return nil, ErrNotSitemap
				}
				rooted = true
			}
			// <loc> is a child of <url>/<sitemap> (depth 3); image and
			// video extensions nest their own <loc> deeper.
			if depth == 3 && t.Name.Local == "loc" {
				inLoc = true
				loc.Reset()
			}
		case xml.CharData:
			if inLoc {
				loc.Write(t)
			}
		case xml.EndElement:
			if inLoc && depth == 3 {
				inLoc = false
				s.Locs = append(s.Locs, strings.TrimSpace(loc.String()))
			}
			depth--
		}
	}
	if !rooted {
		return nil, ErrNotSitemap
	}
	return s, nil
}
