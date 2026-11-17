package generator

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"unicode/utf16"
)

func TestSymlinkOrJunction_FallsBackOnlyOnPrivilegeError(t *testing.T) {
	var junctionTarget string
	junction := func(target, _ string) error { junctionTarget = target; return nil }

	privilege := func(_, _ string) error { return &os.LinkError{Err: errPrivilegeNotHeld} }
	if err := symlinkOrJunction("../rel", `C:\abs`, "link", privilege, junction); err != nil {
		t.Fatal(err)
	}
	if junctionTarget != `C:\abs` {
		t.Errorf("junction target = %q, want the absolute path", junctionTarget)
	}

	junctionTarget = ""
	other := errors.New("disk full")
	if err := symlinkOrJunction("../rel", `C:\abs`, "link", func(_, _ string) error { return other }, junction); !errors.Is(err, other) {
		t.Errorf("err = %v, want the original error", err)
	}
	if junctionTarget != "" {
		t.Error("junction attempted for a non-privilege error")
	}

	if err := symlinkOrJunction("../rel", `C:\abs`, "link", func(_, _ string) error { return nil }, junction); err != nil || junctionTarget != "" {
		t.Errorf("symlink success must not fall back (err=%v)", err)
	}
}

func TestMountPointReparseBuffer_Layout(t *testing.T) {
	const target = `C:\a\b`
	buf := mountPointReparseBuffer(target)
	le := binary.LittleEndian
	if tag := le.Uint32(buf[0:]); tag != 0xA0000003 {
		t.Errorf("tag = %#x", tag)
	}
	if got, want := int(le.Uint16(buf[4:])), len(buf)-8; got != want {
		t.Errorf("ReparseDataLength = %d, want %d", got, want)
	}
	read := func(offField, lenField int) string {
		off, n := int(le.Uint16(buf[offField:])), int(le.Uint16(buf[lenField:]))
		u := make([]uint16, n/2)
		for i := range u {
			u[i] = le.Uint16(buf[16+off+2*i:])
		}
		if le.Uint16(buf[16+off+n:]) != 0 {
			t.Error("name not NUL-terminated")
		}
		return string(utf16.Decode(u))
	}
	if got := read(8, 10); got != `\??\`+target {
		t.Errorf("substitute = %q", got)
	}
	if got := read(12, 14); got != target {
		t.Errorf("print = %q", got)
	}
}

func TestJunctionTarget(t *testing.T) {
	for _, tt := range []struct {
		in, want string
		ok       bool
	}{
		{`C:\repo\web-runtime`, `C:\repo\web-runtime`, true},
		{`d:\x`, `d:\x`, true},
		{`\\?\C:\very\long`, `C:\very\long`, true},
		{`\\server\share\dir`, "", false}, // UNC: junctions cannot target shares
		{`\\?\UNC\server\share`, "", false},
		{`relative\dir`, "", false},
		{`C:`, "", false},
	} {
		got, err := junctionTarget(tt.in)
		if (err == nil) != tt.ok || got != tt.want {
			t.Errorf("junctionTarget(%q) = %q, %v; want %q ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}
