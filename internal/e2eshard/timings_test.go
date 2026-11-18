package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDurationsFromStreamsTakesTheMedianOfTopLevelResults(t *testing.T) {
	a := strings.Join([]string{
		`{"Action":"run","Test":"TestE2EA"}`,
		`{"Action":"pass","Test":"TestE2EA/sub","Elapsed":99}`,
		`{"Action":"pass","Test":"TestE2EA","Elapsed":10.4}`,
		`{"Action":"skip","Test":"TestE2ESkipped","Elapsed":0}`,
		`not json: a toolchain line that escaped test2json`,
		`{"Action":"fail","Test":"TestE2EB","Elapsed":3}`,
		`{"Action":"pass","Elapsed":200}`,
	}, "\n")
	b := `{"Action":"pass","Test":"TestE2EA","Elapsed":30}` + "\n" + `{"Action":"pass","Test":"TestE2EZ","Elapsed":0.2}`
	c := `{"Action":"pass","Test":"TestE2EA","Elapsed":20}`
	got, err := durationsFromStreams(strings.NewReader(a), strings.NewReader(b), strings.NewReader(c))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"TestE2EA": 20, "TestE2EB": 3, "TestE2EZ": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestEncodeTimingsRoundTrips(t *testing.T) {
	in := map[string]int{"TestE2EB": 2, "TestE2EA": 1}
	b, err := encodeTimings(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"TestE2EA\": 1,\n  \"TestE2EB\": 2\n}\n"; string(b) != want {
		t.Fatalf("encoding = %q, want %q", b, want)
	}
	path := filepath.Join(t.TempDir(), "t.json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := readTimings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %v, want %v", out, in)
	}
}

func TestRunTimingsMergesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "//go:build e2e\n\npackage pkg\n\nimport \"testing\"\n\n" +
		"func TestE2EKept(t *testing.T)  {}\nfunc TestE2EFresh(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(pkg, "x_e2e_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, "old.json")
	if err := os.WriteFile(old, []byte(`{"TestE2EKept": 7, "TestE2EFresh": 1, "TestE2EGone": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stream := filepath.Join(dir, "shard.json")
	if err := os.WriteFile(stream, []byte(`{"Action":"pass","Test":"TestE2EFresh","Elapsed":42}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"timings", "-merge", old, "-dir", pkg, stream}, &out); err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"TestE2EFresh\": 42,\n  \"TestE2EKept\": 7\n}\n"; out.String() != want {
		t.Fatalf("timings = %q, want %q (fresh value wins, kept entry survives, gone entry pruned)", out.String(), want)
	}
}

func TestRunTimingsRefusesStreamsWithNoResults(t *testing.T) {
	stream := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(stream, []byte(`{"Action":"start"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"timings", stream}, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error: a timings file built from no results would erase every recorded duration")
	}
}
