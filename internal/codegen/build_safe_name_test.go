package codegen

import (
	"go/build"
	"io"
	"strings"
	"testing"
)

// matches reports whether the go tool would compile the file name on
// goos/goarch, judging the name alone (the content is a bare package clause).
func matches(t *testing.T, name, goos, goarch string) bool {
	t.Helper()
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = goos, goarch
	ctx.OpenFile = func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("package x\n")), nil
	}
	ok, err := ctx.MatchFile(".", name)
	if err != nil {
		t.Fatalf("MatchFile(%s): %v", name, err)
	}
	return ok
}

// Every born file name compiles on every platform, whatever the rpc's last
// word is: ListMaintenanceWindows was born rpc_list_maintenance_windows.go and
// compiled only on Windows (control-plane#2934).
func TestBornRPCFileNamesAreNeverBuildConstrained(t *testing.T) {
	rpcs := []string{"ListMaintenanceWindows", "SyncLinux", "BuildForArm64", "RunWindowsAmd64", "ListJs", "PingWasm"}
	for w := range goKnownOS {
		rpcs = append(rpcs, "List_"+w)
	}
	for w := range goKnownArch {
		rpcs = append(rpcs, "List_"+w)
	}
	for _, rpc := range rpcs {
		for _, name := range []string{RPCHandlerFileName(rpc), ScaffoldTestFileName(rpc)} {
			for _, p := range [][2]string{{"linux", "amd64"}, {"darwin", "arm64"}, {"windows", "386"}} {
				if !matches(t, name, p[0], p[1]) {
					t.Errorf("%s: %s does not compile on %s/%s", rpc, name, p[0], p[1])
				}
			}
		}
	}
}

// An rpc whose name is not a constraint word keeps the name it always had.
func TestBornRPCFileNamesAreUnchangedOtherwise(t *testing.T) {
	for rpc, want := range map[string]string{
		"Echo":                   "rpc_echo.go",
		"ListMaintenanceWindows": "rpc_list_maintenance_windows_rpc.go",
		"ShipOrder":              "rpc_ship_order.go",
		"Darwinism":              "rpc_darwinism.go",
	} {
		if got := RPCHandlerFileName(rpc); got != want {
			t.Errorf("RPCHandlerFileName(%q) = %q, want %q", rpc, got, want)
		}
	}
	if got, want := ScaffoldTestFileName("ListMaintenanceWindows"), "handlers_scaffold_list_maintenance_windows_rpc_test.go"; got != want {
		t.Errorf("ScaffoldTestFileName = %q, want %q", got, want)
	}
}
