package k3dkmsg

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

type recorder struct {
	ran   [][]string
	state map[string]State
	err   error
}

func (r *recorder) run(name string, args ...string) error {
	r.ran = append(r.ran, append([]string{name}, args...))
	if r.err != nil {
		return r.err
	}
	// A successful mknod makes the file exist as the kmsg device.
	if name == "mknod" || (name == "sudo" && len(args) > 2 && args[1] == "mknod") {
		for _, a := range args {
			if strings.HasPrefix(a, "/") {
				r.state[a] = StateKmsg
			}
		}
	}
	return nil
}

func linuxHost(r *recorder, hostKmsg State) Host {
	r.state = map[string]State{HostPath: hostKmsg}
	return Host{
		GOOS:  "linux",
		Euid:  1000,
		Dir:   "/cache/forge/k3d",
		Stat:  func(p string) (State, error) { return r.state[p], nil },
		Run:   r.run,
		Mkdir: func(string) error { return nil },
	}
}

func TestAddArgs_HostHasKmsgChangesNothing(t *testing.T) {
	r := &recorder{}
	in := []string{"cluster", "create", "x", "--servers", "1"}
	out, err := linuxHost(r, StateKmsg).AddArgs(slices.Clone(in))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in, out) {
		t.Fatalf("argv changed on a host with /dev/kmsg: %v", out)
	}
	if len(r.ran) != 0 {
		t.Fatalf("ran %v on a host with /dev/kmsg", r.ran)
	}
}

func TestAddArgs_MissingKmsgMountsDeviceIntoServersAndAgents(t *testing.T) {
	r := &recorder{}
	h := linuxHost(r, StateMissing)
	out, err := h.AddArgs([]string{"cluster", "create", "x"})
	if err != nil {
		t.Fatal(err)
	}
	file := "/cache/forge/k3d/kmsg"
	for _, want := range []string{file + ":/dev/kmsg@server:*", file + ":/dev/kmsg@agent:*"} {
		i := slices.Index(out, want)
		if i < 1 || out[i-1] != "--volume" {
			t.Fatalf("argv %v lacks `--volume %s`", out, want)
		}
	}
	want := []string{"sudo", "-n", "mknod", file, "c", "1", "11"}
	if len(r.ran) != 1 || !slices.Equal(r.ran[0], want) {
		t.Fatalf("ran %v, want exactly %v", r.ran, want)
	}
}

func TestAddArgs_RootCreatesWithoutSudo(t *testing.T) {
	r := &recorder{}
	h := linuxHost(r, StateMissing)
	h.Euid = 0
	if _, err := h.AddArgs(nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"mknod", "/cache/forge/k3d/kmsg", "c", "1", "11"}
	if len(r.ran) != 1 || !slices.Equal(r.ran[0], want) {
		t.Fatalf("ran %v, want %v", r.ran, want)
	}
}

func TestAddArgs_ReusesAnExistingDeviceFile(t *testing.T) {
	r := &recorder{}
	h := linuxHost(r, StateMissing)
	r.state["/cache/forge/k3d/kmsg"] = StateKmsg
	out, err := h.AddArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ran) != 0 || len(out) != 4 {
		t.Fatalf("ran %v, argv %v: an existing device file must be reused untouched", r.ran, out)
	}
}

func TestAddArgs_FailureNamesTheExactCommand(t *testing.T) {
	r := &recorder{err: errors.New("sudo: a password is required")}
	_, err := linuxHost(r, StateMissing).AddArgs(nil)
	if err == nil {
		t.Fatal("want an error when mknod cannot run")
	}
	for _, want := range []string{"/dev/kmsg", "sudo mknod /cache/forge/k3d/kmsg c 1 11", "a password is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestAddArgs_NonDeviceFileInTheWayIsReplaced(t *testing.T) {
	r := &recorder{}
	h := linuxHost(r, StateMissing)
	r.state["/cache/forge/k3d/kmsg"] = StateOther
	var removed []string
	h.Remove = func(p string) error { removed = append(removed, p); delete(r.state, p); return nil }
	if _, err := h.AddArgs(nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(removed, []string{"/cache/forge/k3d/kmsg"}) || len(r.ran) != 1 {
		t.Fatalf("removed %v, ran %v", removed, r.ran)
	}
}

func TestAddArgs_NotApplicableHostsChangeNothing(t *testing.T) {
	for name, mod := range map[string]func(*Host){
		"macOS (docker runs in a VM that has its own /dev/kmsg)": func(h *Host) { h.GOOS = "darwin" },
		"windows":                  func(h *Host) { h.GOOS = "windows" },
		"remote tcp docker daemon": func(h *Host) { h.DockerHost = "tcp://10.0.0.5:2375" },
		"remote ssh docker daemon": func(h *Host) { h.DockerHost = "ssh://builder" },
	} {
		t.Run(name, func(t *testing.T) {
			r := &recorder{}
			h := linuxHost(r, StateMissing)
			mod(&h)
			out, err := h.AddArgs([]string{"a"})
			if err != nil || !slices.Equal(out, []string{"a"}) || len(r.ran) != 0 {
				t.Fatalf("argv %v err %v ran %v", out, err, r.ran)
			}
		})
	}
	t.Run("local unix socket still applies", func(t *testing.T) {
		r := &recorder{}
		h := linuxHost(r, StateMissing)
		h.DockerHost = "unix:///var/run/docker.sock"
		if out, err := h.AddArgs(nil); err != nil || len(out) != 4 {
			t.Fatalf("argv %v err %v", out, err)
		}
	})
}
