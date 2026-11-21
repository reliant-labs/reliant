package codemod

import (
	"strings"
	"testing"
)

func legacyProjectWith(extra map[string]string) map[string]string {
	files := map[string]string{}
	for k, v := range legacyConfigProject {
		files[k] = v
	}
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// A Go test that names the proto package as a string literal: the compiler
// cannot flag it, and it silently matches nothing once the package moves.
func TestConfigProtoPackage_RewritesGoPackageLiterals(t *testing.T) {
	dir := writeTree(t, legacyProjectWith(map[string]string{
		"internal/app/surface_test.go": "package app\n\n" +
			"func surface() {\n" +
			"\tRangeFilesByPackage(\"config.v1\", visit)\n" +
			"\t_ = \"config.v1.AppConfig\"\n" +
			"\t_ = `config.v1.WebConfig`\n" +
			"\t_ = \"acme.config.v1.Other\"\n" +
			"\t_ = \"forge.v1.config\"\n" +
			"\t_ = \"/config.v1.AppConfig\"\n" +
			"\t_ = \"type.googleapis.com/config.v1.AppConfig\"\n" +
			"\t_ = \"config.v1x\"\n" +
			"\t_ = prefix + \"config.v1\" + suffix\n" +
			"}\n",
	}))

	dry, err := runConfigProtoPackage(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := runConfigProtoPackage(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dry.Next, "\n") != strings.Join(rep.Next, "\n") {
		t.Errorf("dry run and run disagree on Next:\n%v\n---\n%v", dry.Next, rep.Next)
	}

	got := readFile(t, dir, "internal/app/surface_test.go")
	for _, want := range []string{
		`RangeFilesByPackage("acme_app.config.v1", visit)`,
		`_ = "acme_app.config.v1.AppConfig"`,
		"_ = `acme_app.config.v1.WebConfig`",
		`_ = "acme.config.v1.Other"`, // another package that merely ends in config.v1
		`_ = "forge.v1.config"`,
		`_ = "config.v1x"`,                       // not the package
		`prefix + "acme_app.config.v1" + suffix`, // exactly the package, wherever it sits
	} {
		if !strings.Contains(got, want) {
			t.Errorf("surface_test.go lacks %q:\n%s", want, got)
		}
	}

	// A literal that only contains the package is a judgement call: left
	// alone, and listed with file:line.
	for _, left := range []string{
		`"/config.v1.AppConfig"`,
		`"type.googleapis.com/config.v1.AppConfig"`,
	} {
		if !strings.Contains(got, left) {
			t.Errorf("surface_test.go rewrote %q, which is not exactly the package:\n%s", left, got)
		}
	}
	next := strings.Join(rep.Next, "\n")
	for _, want := range []string{"internal/app/surface_test.go:9:", "internal/app/surface_test.go:10:"} {
		if !strings.Contains(next, want) {
			t.Errorf("Next lacks %s for a mention the codemod left:\n%s", want, next)
		}
	}
	for _, notWant := range []string{"surface_test.go:4:", "surface_test.go:5:", "surface_test.go:6:", "surface_test.go:7:", "surface_test.go:8:", "surface_test.go:11:", "surface_test.go:12:"} {
		if strings.Contains(next, notWant) {
			t.Errorf("Next lists %s, a line the codemod rewrote or that does not name the package:\n%s", notWant, next)
		}
	}

	again, err := runConfigProtoPackage(dir, Options{Write: true})
	if err != nil || again.Changed() {
		t.Errorf("second run is not a no-op: %+v, %v", again, err)
	}
}

// A Go file with only mentions the codemod cannot rewrite is left byte-identical
// and listed with file:line, so none is silently missed.
func TestConfigProtoPackage_ListsGoLiteralsItCannotRewrite(t *testing.T) {
	const names = "package app\n\nvar name = \"/config.v1.AppConfig\"\n"
	dir := writeTree(t, legacyProjectWith(map[string]string{"internal/app/names.go": names}))

	rep, err := runConfigProtoPackage(dir, Options{Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dir, "internal/app/names.go"); got != names {
		t.Errorf("rewrote a literal that is not exactly the package:\n%s", got)
	}
	if !strings.Contains(strings.Join(rep.Next, "\n"), "internal/app/names.go:3") {
		t.Errorf("names.go:3 not listed: %v", rep.Next)
	}
}
