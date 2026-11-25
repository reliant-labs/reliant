package templates

import "strings"

// PGOBuildArg is the Dockerfile ARG that sets one binary's `go build -pgo=`
// in the scaffolded Dockerfile (project/Dockerfile.tmpl): FORGE_PGO_ plus
// the cmd/<dir> leaf in upper snake case ("my-app" -> FORGE_PGO_MY_APP).
// It defaults to `auto` (Go's own default, which reads cmd/<dir>/default.pgo
// when it exists); `forge build` passes `--build-arg <name>=off` for a binary
// whose KCL PGO is disabled. The template and the build both spell the name
// through this function, so they cannot disagree.
func PGOBuildArg(dir string) string {
	var b strings.Builder
	b.WriteString("FORGE_PGO_")
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
