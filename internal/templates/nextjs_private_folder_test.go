package templates

import (
	"strings"
	"testing"
)

// TestNextjsTree_NoPrivateRouteFolders pins that no folder under the Next.js
// scaffold's src/app/ begins with "_".
//
// The App Router treats a `_`-prefixed folder as PRIVATE: it and everything
// beneath it are excluded from routing. A route handler placed there compiles
// cleanly, type-checks, and answers 404 to every request — which is exactly
// how the dev-log receiver shipped as `src/app/__forge/log/route.ts` and
// dropped every browser console line on the floor. A URL segment that must
// start with an underscore is spelled with the encoded escape, `%5F`.
func TestNextjsTree_NoPrivateRouteFolders(t *testing.T) {
	files, err := ListFrontendTree("nextjs")
	if err != nil {
		t.Fatalf("ListFrontendTree: %v", err)
	}
	sawDevLog := false
	for _, f := range files {
		if !strings.HasPrefix(f.Rel, "src/app/") {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(f.Rel, "src/app/"), "/")
		for _, seg := range segs[:len(segs)-1] { // directories only
			if strings.HasPrefix(seg, "_") {
				t.Errorf("%s: folder %q is private to the App Router and is never routed; spell a leading underscore as %%5F", f.Rel, seg)
			}
		}
		if f.Rel == "src/app/%5F_forge/log/route.ts" {
			sawDevLog = true
		}
	}
	if !sawDevLog {
		t.Errorf("dev-log receiver src/app/%%5F_forge/log/route.ts (serves POST /__forge/log) missing from the nextjs tree")
	}
}
