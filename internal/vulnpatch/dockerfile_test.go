package vulnpatch

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseDockerfile(t *testing.T) {
	t.Parallel()
	src := "# syntax=docker/dockerfile:1\n" +
		"ARG GO=1.22.3\n" +
		"FROM --platform=$BUILDPLATFORM golang:1.22.3-alpine3.19 AS Build\n" +
		"RUN go build \\\n" +
		"  # a comment inside a continuation\n" +
		"  ./...\n" +
		"from build as test\n" +
		"FROM \\\n" +
		"  alpine:3.19.1 AS final\n" +
		"COPY --from=build /out /app\n"
	d := parseDockerfile("Dockerfile", "/x/Dockerfile", []byte(src))
	if len(d.stages) != 3 {
		t.Fatalf("stages = %+v", d.stages)
	}
	want := []stage{
		{line: 3, image: "golang:1.22.3-alpine3.19", name: "build", platformVariable: "--platform=$BUILDPLATFORM"},
		{line: 7, image: "build", name: "test"},
		{line: 9, image: "alpine:3.19.1", name: "final"},
	}
	for i, w := range want {
		if *d.stages[i] != w {
			t.Errorf("stage %d = %+v, want %+v", i, *d.stages[i], w)
		}
	}
	if base, reason := d.finalBase(); base != d.stages[2] || reason != "" {
		t.Errorf("finalBase = %+v %q", base, reason)
	}
	// The test stage follows its alias to the golang base.
	if got := d.baseOf(1); got != d.stages[0] {
		t.Errorf("baseOf(test) = %+v", got)
	}
}

// A variable in a FROM's image, or in a flag other than --platform, makes
// the stage opaque. A variable --platform picks an architecture of the
// literal base and is only recorded; a literal one is read.
func TestParseFromVariablesAndPlatform(t *testing.T) {
	t.Parallel()
	for src, want := range map[string]stage{
		"FROM --platform=$BUILDPLATFORM alpine:3.19":      {image: "alpine:3.19", platformVariable: "--platform=$BUILDPLATFORM"},
		"FROM --platform=${TARGETPLATFORM} alpine:3.19":   {image: "alpine:3.19", platformVariable: "--platform=${TARGETPLATFORM}"},
		"FROM --platform=linux/${ARCH} alpine:3.19":       {image: "alpine:3.19", platformVariable: "--platform=linux/${ARCH}"},
		"FROM --platform=linux/amd64 alpine:3.19":         {image: "alpine:3.19", platform: "linux/amd64"},
		"FROM --platform=Linux/ARM64/v8 alpine:3.19 AS x": {image: "alpine:3.19", platform: "linux/arm64/v8", name: "x"},
		"FROM --platform=linux/amd64 alpine:${V}":         {image: "alpine:${V}", variable: "alpine:${V}", platform: "linux/amd64"},
		"FROM alpine@${DIGEST}":                           {image: "alpine@${DIGEST}", variable: "alpine@${DIGEST}"},
		"FROM ${REGISTRY}/alpine:3.19":                    {image: "${REGISTRY}/alpine:3.19", variable: "${REGISTRY}/alpine:3.19"},
		"FROM --future=$X alpine:3.19":                    {image: "alpine:3.19", variable: "--future=$X"},
		"FROM alpine:3.19":                                {image: "alpine:3.19"},
	} {
		d := parseDockerfile("Dockerfile", "/x/Dockerfile", []byte(src+"\n"))
		want.line = 1
		if len(d.stages) != 1 || *d.stages[0] != want {
			t.Errorf("%s: stages %+v, want %+v", src, d.stages, want)
			continue
		}
		if d.stages[0].opaque() != (want.variable != "") {
			t.Errorf("%s: opaque = %v", src, d.stages[0].opaque())
		}
	}
}

func TestDockerfileFinalBase(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		src, image, reason string
		none               bool
	}{
		"alias":                         {src: "FROM debian:12 AS base\nFROM base\n", image: "debian:12"},
		"scratch":                       {src: "FROM golang:1.22 AS b\nFROM scratch\nCOPY --from=b /x /x\n", none: true},
		"arg":                           {src: "ARG BASE=alpine:3.19\nFROM ${BASE}\n", image: "${BASE}", reason: "build argument"},
		"arg tag":                       {src: "ARG V=3.19\nFROM alpine:${V}\n", image: "alpine:${V}", reason: "build argument"},
		"arg flag":                      {src: "FROM --future=$X alpine:3.19\n", image: "alpine:3.19", reason: "FROM flag expands a build argument"},
		"arg platform":                  {src: "FROM --platform=$BUILDPLATFORM alpine:3.19\n", image: "alpine:3.19"},
		"arg platform through an alias": {src: "FROM --platform=${TARGETPLATFORM} debian:12 AS base\nFROM base\n", image: "debian:12"},
		"no from":                       {src: "RUN true\n", none: true, reason: "has no FROM"},
		"escape":                        {src: "# escape=`\nFROM `\n  node:20-alpine\n", image: "node:20-alpine"},
		"lower case":                    {src: "from ubuntu:22.04 as x\n", image: "ubuntu:22.04"},
	} {
		d := parseDockerfile("Dockerfile", "/x/Dockerfile", []byte(tc.src))
		base, reason := d.finalBase()
		switch {
		case tc.none && base != nil:
			t.Errorf("%s: base = %+v, want none", name, base)
		case !tc.none && (base == nil || base.image != tc.image):
			t.Errorf("%s: base = %+v, want %s", name, base, tc.image)
		}
		if tc.reason == "" && reason != "" || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: reason = %q, want %q", name, reason, tc.reason)
		}
	}
}

func TestParseImageRef(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]imageRef{
		"alpine":              {name: "alpine", host: "docker.io", path: "library/alpine"},
		"alpine:3.19":         {name: "alpine", host: "docker.io", path: "library/alpine", tag: "3.19"},
		"bitnami/redis:7.2.4": {name: "bitnami/redis", host: "docker.io", path: "bitnami/redis", tag: "7.2.4"},
		"gcr.io/distroless/static-debian12:nonroot@sha256:" + strings.Repeat("a", 64): {name: "gcr.io/distroless/static-debian12", host: "gcr.io", path: "distroless/static-debian12", tag: "nonroot", digest: "sha256:" + strings.Repeat("a", 64)},
		"localhost:5000/team/app:1": {name: "localhost:5000/team/app", host: "localhost:5000", path: "team/app", tag: "1"},
		"localhost/app":             {name: "localhost/app", host: "localhost", path: "app"},
	} {
		got, err := parseImageRef(in)
		if err != nil || got != want {
			t.Errorf("parseImageRef(%q) = %+v, %v; want %+v", in, got, err, want)
		}
		if got.String() != in {
			t.Errorf("String() = %q, want %q", got.String(), in)
		}
	}
	for _, bad := range []string{"${BASE}", "alpine@md5:abc", ""} {
		if _, err := parseImageRef(bad); err == nil {
			t.Errorf("parseImageRef(%q) accepted", bad)
		}
	}
}

func TestEditFromTouchesOnlyTheImage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "Dockerfile")
	src := "FROM golang:1.22.3 AS build\r\nRUN echo golang:1.22.3\r\nFROM --platform=linux/amd64 golang:1.22.3\r\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := editFrom(path, 3, "golang:1.22.3", "golang:1.22.5"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "FROM golang:1.22.3 AS build\r\nRUN echo golang:1.22.3\r\nFROM --platform=linux/amd64 golang:1.22.5\r\n"
	if string(got) != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	// Windows has no permission bits to keep.
	if info, _ := os.Stat(path); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode())
	}
	if err := editFrom(path, 1, "golang:1.21", "x"); err == nil || !strings.Contains(err.Error(), "no longer names golang:1.21") {
		t.Errorf("a line that changed since the plan: %v", err)
	}
}

func TestIsDockerfileName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		"Dockerfile": true, "dockerfile": true, "Containerfile": true, "Dockerfile.prod": true, "api.Dockerfile": true,
		"Dockerfile.dockerignore": false, ".dockerignore": false, "docker-compose.yml": false, "Dockerfiles": false,
	} {
		if got := isDockerfileName(name); got != want {
			t.Errorf("isDockerfileName(%q) = %v", name, got)
		}
	}
}
