package kclprojects

import (
	"path/filepath"
	"strings"
	"testing"
)

// LocateModule is the [forge_projects] resolution offered to any reader of
// another project's files: the same pin, the same precedence, and "not
// pinned" as an answer rather than an error.
func TestLocateModule(t *testing.T) {
	t.Setenv("GOWORK", "off")

	t.Run("nothing pins it", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "consumer")
		write(t, dir, map[string]string{"go.mod": "module example.com/consumer\n\ngo 1.22\n"})
		if loc, ok, err := LocateModule(dir, producerMod, "test"); ok || err != nil {
			t.Fatalf("an unrequired module: got %+v, %v, %v; want not pinned", loc, ok, err)
		}
		if _, ok, err := LocateModule(t.TempDir(), producerMod, "test"); ok || err != nil {
			t.Fatalf("no go.mod: got %v, %v; want not pinned", ok, err)
		}
	})

	t.Run("the module cache at the required version", func(t *testing.T) {
		root := t.TempDir()
		cache := fakeCache(t, producerMod, "v1.2.3", "h1:good", producerFiles(producerMod, pinA))
		dir := filepath.Join(root, "consumer")
		write(t, dir, map[string]string{
			"go.mod": "module example.com/consumer\n\ngo 1.22\n\nrequire " + producerMod + " v1.2.3\n",
			"go.sum": producerMod + " v1.2.3 h1:good\n",
		})
		// Asked from below the go.mod, as a caller in a subdirectory would.
		loc, ok, err := LocateModule(filepath.Join(dir, "deploy"), producerMod, "test")
		if err != nil || !ok {
			t.Fatalf("got %v, %v", ok, err)
		}
		if loc.Dir != filepath.Join(cache, producerMod+"@v1.2.3") || loc.Version != "v1.2.3" || !strings.Contains(loc.Via, "go.mod require "+producerMod+" v1.2.3") {
			t.Fatalf("got %+v", loc)
		}
	})

	t.Run("a directory replace is read in place", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "producer"), producerFiles(producerMod, pinA))
		dir := filepath.Join(root, "consumer")
		write(t, dir, map[string]string{
			"go.mod": "module example.com/consumer\n\ngo 1.22\n\nrequire " + producerMod + " v1.2.3\n\nreplace " + producerMod + " => ../producer\n",
		})
		loc, ok, err := LocateModule(dir, producerMod, "test")
		if err != nil || !ok || loc.Dir != filepath.Join(root, "producer") || loc.Version != "" {
			t.Fatalf("got %+v, %v, %v", loc, ok, err)
		}
	})

	t.Run("an unverifiable cache entry is refused", func(t *testing.T) {
		root := t.TempDir()
		fakeCache(t, producerMod, "v1.2.3", "h1:tampered", producerFiles(producerMod, pinA))
		dir := filepath.Join(root, "consumer")
		write(t, dir, map[string]string{
			"go.mod": "module example.com/consumer\n\ngo 1.22\n\nrequire " + producerMod + " v1.2.3\n",
			"go.sum": producerMod + " v1.2.3 h1:good\n",
		})
		_, _, err := LocateModule(dir, producerMod, "the alert inventory")
		wantErr(t, err, "does not match go.sum")
	})
}
