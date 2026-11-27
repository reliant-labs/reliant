package siteaudit

import "bytes"

// PlaceholderImageMarker is the byte string forge's scaffolded placeholder
// images carry (a PNG tEXt chunk, an SVG comment), so an audit can name a
// site that still ships one (og_image_placeholder) without knowing the
// image's exact bytes, which a scaffold may change.
const PlaceholderImageMarker = "forge:placeholder-image"

// placeholderScanBytes bounds how much of an image IsPlaceholderImage reads:
// the marker sits in the header, before the pixel data.
const placeholderScanBytes = 64 * 1024

// IsPlaceholderImage reports whether an image's bytes are forge's scaffolded
// placeholder. A caller that fetched an og:image sets
// [LinkResult.Placeholder] from it.
func IsPlaceholderImage(b []byte) bool {
	if len(b) > placeholderScanBytes {
		b = b[:placeholderScanBytes]
	}
	return bytes.Contains(b, []byte(PlaceholderImageMarker))
}
