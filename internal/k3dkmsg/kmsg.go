// Package k3dkmsg lets `k3d cluster create` work on a Linux host that has no
// /dev/kmsg, as a kata or gVisor sandbox does.
//
// k3s's kubelet opens /dev/kmsg for its OOM watcher and exits fatally when it
// cannot ("open /dev/kmsg: no such file or directory"). k3s then shuts down
// about two seconds after it started, the k3d entrypoint keeps the container
// "running", `--wait` hangs for its whole timeout, and :6443 refuses
// connections — which reads as a networking failure and is not one.
//
// Docker's --privileged copies the HOST's /dev nodes into a container, so a
// host without /dev/kmsg gives the node none either. The fix is a character
// device (1:11) made by mknod under forge's cache directory and bind-mounted
// over /dev/kmsg in every server and agent node. A host that has /dev/kmsg is
// left exactly as it is.
package k3dkmsg

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// HostPath is the device k3s's kubelet needs.
const HostPath = "/dev/kmsg"

// State is what a path is, as far as kmsg is concerned.
type State int

const (
	// StateMissing means the device path does not exist.
	StateMissing State = iota
	// StateKmsg is a character device with the kernel's kmsg numbers (1:11).
	StateKmsg
	// StateOther is anything else: a regular file, or a different device.
	StateOther
)

// Host is everything AddArgs reads from or does to the machine, so a test
// supplies all of it.
type Host struct {
	GOOS       string
	Euid       int
	DockerHost string
	// Dir is the forge-owned directory the device file is made in.
	Dir    string
	Stat   func(path string) (State, error)
	Run    func(name string, args ...string) error
	Remove func(path string) error
	Mkdir  func(path string) error
}

// AddArgs adds the kmsg mount to a `k3d cluster create` argv, creating the
// device file first when it is needed. It returns args unchanged on a host
// whose /dev/kmsg exists, and on one where the question does not arise.
func AddArgs(args []string) ([]string, error) {
	h, err := OSHost()
	if err != nil {
		return nil, err
	}
	return h.AddArgs(args)
}

// OSHost is the real machine.
func OSHost() (Host, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return Host{}, fmt.Errorf("k3dkmsg: no cache directory for the %s device file: %w", HostPath, err)
	}
	return Host{
		GOOS:       runtime.GOOS,
		Euid:       os.Geteuid(),
		DockerHost: os.Getenv("DOCKER_HOST"),
		Dir:        filepath.Join(cache, "forge", "k3d"),
		Stat:       statDevice,
		Run:        runCombined,
		Remove:     os.Remove,
		Mkdir:      func(p string) error { return os.MkdirAll(p, 0o755) },
	}, nil
}

func runCombined(name string, args ...string) error {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AddArgs is the package-level AddArgs for an explicit Host.
func (h Host) AddArgs(args []string) ([]string, error) {
	if !h.needed() {
		return args, nil
	}
	// path, not filepath: the device file is made on, and mounted from, a
	// Linux host (needed is false everywhere else), whatever OS runs forge.
	file := path.Join(h.Dir, "kmsg")
	if err := h.ensure(file); err != nil {
		return nil, err
	}
	// server:* and agent:* rather than all: the load balancer and the tools
	// node run no kubelet. An agent filter that matches nothing is accepted.
	return append(args,
		"--volume", file+":"+HostPath+"@server:*",
		"--volume", file+":"+HostPath+"@agent:*"), nil
}

// needed reports whether this host is the one the workaround is for: a local
// Linux docker daemon on a machine without /dev/kmsg. macOS and Windows run
// docker in a VM whose /dev/kmsg the host never sees, and a remote daemon's
// /dev is not this machine's.
func (h Host) needed() bool {
	if h.GOOS != "linux" {
		return false
	}
	if h.DockerHost != "" && !strings.HasPrefix(h.DockerHost, "unix://") {
		return false
	}
	st, err := h.Stat(HostPath)
	return err == nil && st == StateMissing
}

// ensure makes file a kmsg device, reusing one that already is.
func (h Host) ensure(file string) error {
	st, err := h.Stat(file)
	if err != nil {
		return fmt.Errorf("%w", err)
	}
	if st == StateKmsg {
		return nil
	}
	if st == StateOther {
		if err := h.Remove(file); err != nil {
			return h.failure(file, fmt.Errorf("remove the stale %s: %w", file, err), "sudo rm -f "+file+" && ")
		}
	}
	if err := h.Mkdir(h.Dir); err != nil {
		return h.failure(file, err, "mkdir -p "+h.Dir+" && ")
	}
	name, args := "mknod", []string{file, "c", "1", "11"}
	if h.Euid != 0 {
		name, args = "sudo", append([]string{"-n", "mknod"}, args...)
	}
	if err := h.Run(name, args...); err != nil {
		return h.failure(file, err, "")
	}
	return nil
}

func (h Host) failure(file string, cause error, prefix string) error {
	return fmt.Errorf("this host has no %s, and k3s's kubelet refuses to start without it, so a k3d cluster cannot boot "+
		"(it looks like a networking failure: the API server on :6443 refuses connections).\n"+
		"forge could not create the replacement device file: %v\n"+
		"Create it yourself, then retry:\n    %ssudo mknod %s c 1 11", HostPath, cause, prefix, file)
}
