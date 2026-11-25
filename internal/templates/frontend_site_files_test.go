package templates

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// Every browser frontend is scaffolded with the files a browser or crawler
// asks a fresh site for, so they answer instead of 404ing: an icon,
// /favicon.ico (which clients request without reading the page's <link>),
// and /robots.txt. All three are the project's to replace.
func TestBrowserFrontendsScaffoldIconFaviconAndRobots(t *testing.T) {
	icon := map[string]string{"nextjs": "src/app/icon.svg", "vite-spa": "public/icon.svg"}
	for _, kind := range []string{"nextjs", "vite-spa"} {
		files, err := ListFrontendTree(kind)
		if err != nil {
			t.Fatalf("ListFrontendTree(%q): %v", kind, err)
		}
		tree := map[string]string{}
		for _, f := range files {
			tree[f.Rel] = f.Path
		}
		for _, rel := range []string{icon[kind], "public/favicon.ico", "public/robots.txt"} {
			if tree[rel] == "" {
				t.Errorf("%s: the scaffolded tree has no %s", kind, rel)
			}
		}
		// Next.js serves src/app/icon.svg as a route; a public/icon.svg beside
		// it would be a second file at the same path.
		if kind == "nextjs" && tree["public/icon.svg"] != "" {
			t.Errorf("nextjs: public/icon.svg conflicts with the src/app/icon.svg route")
		}
	}

	// One mark: the Vite tree's icon is the Next.js one, byte for byte.
	next, err := FrontendTemplates().Get("nextjs/src/app/icon.svg")
	if err != nil {
		t.Fatal(err)
	}
	vite, err := FrontendTemplates().Get("vite-spa/public/icon.svg")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(next, vite) {
		t.Error("vite-spa/public/icon.svg drifted from nextjs/src/app/icon.svg")
	}

	// A Vite page has no file-based icon convention: index.html links it.
	html, err := FrontendTemplates().Get("vite-spa/index.html.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), `<link rel="icon" type="image/svg+xml" href="%BASE_URL%icon.svg" />`) {
		t.Error("vite-spa/index.html does not link public/icon.svg")
	}

	robots, err := FrontendTemplates().Get("shared-web/public/robots.txt")
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for _, line := range strings.Split(string(robots), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	if strings.Join(rules, "\n") != "User-agent: *\nAllow: /" {
		t.Errorf("robots.txt rules = %q, want every crawler allowed everywhere", rules)
	}
}

// public/favicon.ico is a real ICO: the header, then one directory entry per
// image, each a PNG at the size the entry declares (16, 32 and 48 px).
func TestFaviconIsAValidICO(t *testing.T) {
	ico, err := FrontendTemplates().Get("shared-web/public/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(ico) < 6 || binary.LittleEndian.Uint16(ico[0:]) != 0 || binary.LittleEndian.Uint16(ico[2:]) != 1 {
		t.Fatalf("favicon.ico has no ICO header: % x", ico[:min(len(ico), 6)])
	}
	n := int(binary.LittleEndian.Uint16(ico[4:]))
	var sizes []int
	for i := 0; i < n; i++ {
		e := ico[6+16*i:]
		size := int(e[0])
		if size == 0 {
			size = 256
		}
		length, offset := binary.LittleEndian.Uint32(e[8:]), binary.LittleEndian.Uint32(e[12:])
		if int(offset)+int(length) > len(ico) {
			t.Fatalf("entry %d points past the file", i)
		}
		png := ico[offset : offset+length]
		if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) || string(png[12:16]) != "IHDR" {
			t.Fatalf("entry %d is not a PNG", i)
		}
		if w, h := binary.BigEndian.Uint32(png[16:]), binary.BigEndian.Uint32(png[20:]); int(w) != size || int(h) != size {
			t.Errorf("entry %d declares %dpx but its PNG is %dx%d", i, size, w, h)
		}
		sizes = append(sizes, size)
	}
	if len(sizes) != 3 || sizes[0] != 16 || sizes[1] != 32 || sizes[2] != 48 {
		t.Errorf("favicon.ico sizes = %v, want [16 32 48]", sizes)
	}
}
