package deploytarget

import (
	"context"
	"reflect"
	"testing"
)

// runInDir must exec argv directly with Dir set: no `sh`, no quoting, so a dir
// with spaces and an arg with metacharacters arrive untouched.
func TestRunInDir_DirectExecNoShell(t *testing.T) {
	fake := &fakeRunner{}
	dir := "/tmp/my project/web app"
	err := runInDir(context.Background(), fake, dir, map[string]string{"K": "v"}, []string{"npm", "run", "build", "--flag=a b;c"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"npm run build --flag=a b;c"}; !reflect.DeepEqual(fake.calls, want) {
		t.Errorf("calls = %q, want %q", fake.calls, want)
	}
	if !reflect.DeepEqual(fake.dirCalls, []string{dir}) {
		t.Errorf("dirCalls = %q", fake.dirCalls)
	}
	if fake.envCalls[0]["K"] != "v" {
		t.Errorf("env not forwarded: %v", fake.envCalls[0])
	}
}
