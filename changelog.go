// Package forge holds files at the root of forge's module that the binary
// embeds.
//
// CHANGELOG.md is embedded so `forge project upgrade` can say what the
// releases a pin bump crosses brought, offline, from the forge that runs it.
// The release script writes the file at the tag (scripts/changelog-release.sh),
// so every released binary carries its own release's section and every
// earlier one with no extra step.
package forge

import _ "embed"

//go:embed CHANGELOG.md
var changelog []byte

// Changelog returns forge's CHANGELOG.md as this binary was built with it.
func Changelog() []byte { return changelog }
