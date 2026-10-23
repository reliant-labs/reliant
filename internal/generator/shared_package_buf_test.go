package generator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestSharedProtoPackageAddsBufExceptions(t *testing.T) {
	for _, except := range []string{"", "  except:\n    - PACKAGE_SAME_DIRECTORY # user's exception\n", "  except: [PACKAGE_VERSION_SUFFIX] # retain this\n"} {
		t.Run(except, func(t *testing.T) {
			root := t.TempDir()
			writeServiceProto(t, root, "trading", "trader.v1")
			before := "version: v2\nmodules:\n  - path: proto\nlint:\n  use: [STANDARD] # user's choice\n" + except + "breaking:\n  use: [FILE]\n"
			path := filepath.Join(root, "buf.yaml")
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := GenerateServiceFilesWithMode(root, "example.com/app", "access", "app", ScaffoldFail, &out); err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			for _, want := range []string{"PACKAGE_SAME_DIRECTORY", "PACKAGE_SAME_GO_PACKAGE", "# user's choice", "breaking:\n  use: [FILE]"} {
				if !strings.Contains(string(got), want) {
					t.Errorf("missing %q:\n%s", want, got)
				}
			}
			if !strings.Contains(out.String(), "buf.yaml") {
				t.Errorf("no configuration notice: %s", out.String())
			}
			if err := GenerateServiceFilesWithMode(root, "example.com/app", "access", "app", ScaffoldResume, &out); err != nil {
				t.Fatal(err)
			}
			again, _ := os.ReadFile(path)
			if string(again) != string(got) {
				t.Fatal("not idempotent")
			}
		})
	}
}

func TestSharedProtoPackageDisownedBufWarns(t *testing.T) {
	root := t.TempDir()
	writeServiceProto(t, root, "trading", "trader.v1")
	before := "version: v2\nlint:\n  use: [STANDARD]\n"
	path := filepath.Join(root, "buf.yaml")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{"buf.yaml": {}}}
	if err := checksums.Save(root, cs); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := GenerateServiceFilesWithMode(root, "example.com/app", "access", "app", ScaffoldFail, &out); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != before {
		t.Fatal("disowned buf.yaml changed")
	}
	for _, want := range []string{"disowned", "PACKAGE_SAME_DIRECTORY", "PACKAGE_SAME_GO_PACKAGE"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("warning missing %s: %s", want, out.String())
		}
	}
}
