package selfupdate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// managedBy, when stamped at build time, marks every copy of this binary as
// owned by something else, which [Detect] then never overrides:
//
//	go build -ldflags "-X github.com/reliant-labs/forge/pkg/selfupdate.managedBy=homebrew"
//
// Packagers (Homebrew, AUR, deb, rpm) and supervisors set it. A file
// `<binary>.managed-by` beside the binary, or FORGE_MANAGED_BY in the
// environment, says the same without a rebuild. The linker flag is the
// contract, so the variable stays unexported: nothing in Go may change it.
var managedBy string

// ManagedByEnv is the environment variable a supervisor sets to mark the
// binary it runs as owned by it.
const ManagedByEnv = "FORGE_MANAGED_BY"

// Kind is who may replace an install.
type Kind int

const (
	// Unknown is the zero Kind; it never permits a swap.
	Unknown Kind = iota
	// SelfOwned: nothing else owns the install, so it may replace itself.
	SelfOwned
	// Stamped: a managed-by stamp names the owner. Authoritative.
	Stamped
	// Bundle: the binary sits inside an app bundle or AppImage, whose own
	// updater replaces it.
	Bundle
	// PackageManager: Homebrew, dpkg, rpm, pacman, snap or nix installed it.
	PackageManager
	// Managed: something else runs it and replaces it: the app's own
	// predicate, a Kubernetes pod or a container (the image is the unit).
	Managed
	// ReadOnly: this user cannot write the binary's directory.
	ReadOnly
)

func (k Kind) String() string {
	switch k {
	case SelfOwned:
		return "self-owned"
	case Stamped:
		return "stamped"
	case Bundle:
		return "bundle"
	case PackageManager:
		return "package-manager"
	case Managed:
		return "managed"
	case ReadOnly:
		return "read-only"
	}
	return "unknown"
}

// Install is what [Detect] found about the running binary.
type Install struct {
	// Path is the executable, symlinks resolved.
	Path string
	Kind Kind
	// Owner names who owns it when Kind is not SelfOwned ("homebrew",
	// "dpkg:reliant", the stamp's value …).
	Owner string
	// Hint is the command that updates it, when one is known.
	Hint string
	// Reason says which signal decided Kind.
	Reason string
}

// SelfOwned reports whether the install may replace itself.
func (i Install) SelfOwned() bool { return i.Kind == SelfOwned }

// ErrNotSelfOwned is matched (errors.Is) by the error [Updater.Prepare] and
// [Updater.ExecPending] return for an install something else owns. errors.As
// to *NotSelfOwnedError reaches the [Install].
var ErrNotSelfOwned = errors.New("selfupdate: install is owned by something else")

// NotSelfOwnedError carries the refused [Install].
type NotSelfOwnedError struct{ Install Install }

func (e *NotSelfOwnedError) Error() string {
	msg := fmt.Sprintf("selfupdate: %s is not self-owned (%s", e.Install.Path, e.Install.Kind)
	if e.Install.Owner != "" {
		msg += ", owner " + e.Install.Owner
	}
	msg += "): " + e.Install.Reason
	if e.Install.Hint != "" {
		msg += "; update it with: " + e.Install.Hint
	}
	return msg
}

// Is makes errors.Is(err, ErrNotSelfOwned) hold.
func (e *NotSelfOwnedError) Is(target error) bool { return target == ErrNotSelfOwned }

// DetectOptions tunes [Detect]. The zero value inspects the running binary.
type DetectOptions struct {
	// Path is the binary to inspect; default os.Executable().
	Path string
	// Managed is the app's own "something else runs me" predicate (Reliant:
	// host.Managed(), true in a cloud pod). It returns the owner's name.
	Managed func() (owner string, managed bool)
}

// Detect resolves the binary and says who may replace it. It refuses on any
// signal and grants only when none fires.
func Detect(opts DetectOptions) (Install, error) {
	return detect(opts, defaultDetectEnv())
}

func defaultDetectEnv() detectEnv {
	return detectEnv{
		stamp:     managedBy,
		lookupEnv: lookupManagedByEnv,
		runOwner:  queryPackageOwner,
		container: inContainer,
	}
}

// detectEnv is Detect's seams: the build stamp, the environment, the
// package-manager query and the container probe, so tests need none of them.
type detectEnv struct {
	stamp     string
	lookupEnv func() (string, bool)
	runOwner  func(path string) (owner, hint string, ok bool)
	container func() (owner, hint, reason string, ok bool)
}

func detect(opts DetectOptions, env detectEnv) (Install, error) {
	path := opts.Path
	if path == "" {
		exe, err := os.Executable()
		if err != nil {
			return Install{}, fmt.Errorf("selfupdate: locate the running binary: %w", err)
		}
		path = exe
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Install{}, fmt.Errorf("selfupdate: resolve %s: %w", path, err)
	}
	if resolved, err = filepath.Abs(resolved); err != nil {
		return Install{}, fmt.Errorf("selfupdate: resolve %s: %w", path, err)
	}
	in := Install{Path: resolved}

	// 1. Stamps: authoritative, so first.
	if owner := strings.TrimSpace(env.stamp); owner != "" {
		return refuse(in, Stamped, owner, "", "built with -X selfupdate.managedBy="+owner), nil
	}
	if owner, ok := readStampFile(resolved + ".managed-by"); ok {
		return refuse(in, Stamped, owner, "", "a "+filepath.Base(resolved)+".managed-by file beside the binary names "+owner), nil
	}
	if env.lookupEnv != nil {
		if owner, ok := env.lookupEnv(); ok && strings.TrimSpace(owner) != "" {
			owner = strings.TrimSpace(owner)
			return refuse(in, Stamped, owner, "", ManagedByEnv+"="+owner+" in the environment"), nil
		}
	}
	// 2. The app's own predicate.
	if opts.Managed != nil {
		if owner, managed := opts.Managed(); managed {
			return refuse(in, Managed, owner, "", "the app reports a managed host"), nil
		}
	}
	// 3. A pod or a container: its image is the unit of upgrade, and the
	// platform that runs it rolls a new one. A binary that replaced itself
	// there would be undone by the next restart, and would make the running
	// pod disagree with the image it reports.
	if env.container != nil {
		if owner, hint, reason, ok := env.container(); ok {
			return refuse(in, Managed, owner, hint, reason), nil
		}
	}
	// 4. Path heuristics.
	slash := filepath.ToSlash(resolved)
	if reason, ok := bundlePath(slash); ok {
		return refuse(in, Bundle, "app bundle", "", reason), nil
	}
	if owner, hint, reason, ok := packageManagerPath(slash); ok {
		return refuse(in, PackageManager, owner, hint, reason), nil
	}
	if systemPath(slash) && env.runOwner != nil {
		if owner, hint, ok := env.runOwner(resolved); ok {
			return refuse(in, PackageManager, owner, hint, "the system package database owns "+resolved), nil
		}
	}
	// 5. Writability: the swap renames in the directory, so the directory
	// must be writable, not just the file.
	if err := probeWritable(filepath.Dir(resolved)); err != nil {
		return refuse(in, ReadOnly, "", "", "cannot write "+filepath.Dir(resolved)+": "+err.Error()), nil
	}
	in.Kind = SelfOwned
	in.Reason = "no owner detected and the directory is writable"
	return in, nil
}

func refuse(in Install, k Kind, owner, hint, reason string) Install {
	in.Kind, in.Owner, in.Hint, in.Reason = k, owner, hint, reason
	return in
}

func readStampFile(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			return line, true
		}
	}
	// An empty stamp file still says "not yours".
	return "unnamed owner", true
}

// containerMarkers are the files container engines create in every
// container: Docker's and Podman's.
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// inContainer reports a Kubernetes pod (the kubelet sets
// KUBERNETES_SERVICE_HOST in every container it starts) or a Docker or
// Podman container (their marker files).
func inContainer() (owner, hint, reason string, ok bool) {
	if v, set := lookupKubernetesEnv(); set && strings.TrimSpace(v) != "" {
		return "kubernetes", "roll the workload to an image with the new release",
			"running in a Kubernetes pod (" + KubernetesServiceHostEnv + " is set): the image is the unit of upgrade", true
	}
	for _, m := range containerMarkers {
		if _, err := os.Stat(m); err == nil {
			return "container", "pull and run an image with the new release",
				"running in a container (" + m + " exists): the image is the unit of upgrade", true
		}
	}
	return "", "", "", false
}

// bundlePath matches a binary inside a macOS .app, an Electron resources
// directory, or a mounted AppImage.
func bundlePath(p string) (string, bool) {
	lower := strings.ToLower(p)
	switch {
	case strings.Contains(lower, ".app/contents/"):
		return "inside a macOS .app bundle", true
	case strings.Contains(lower, "/resources/server/"), strings.Contains(lower, "/resources/app.asar"):
		return "inside an Electron app's resources", true
	case strings.HasPrefix(p, "/tmp/.mount_"):
		return "inside a mounted AppImage", true
	}
	return "", false
}

// packageManagerPath matches the prefixes package managers own outright.
func packageManagerPath(p string) (owner, hint, reason string, ok bool) {
	for _, marker := range []string{"/Cellar/", "/Caskroom/"} {
		if i := strings.Index(p, marker); i >= 0 {
			name := strings.SplitN(p[i+len(marker):], "/", 2)[0]
			return "homebrew", "brew upgrade " + name, "installed under Homebrew's " + strings.Trim(marker, "/"), true
		}
	}
	switch {
	case strings.HasPrefix(p, "/home/linuxbrew/"), strings.HasPrefix(p, "/opt/homebrew/"):
		return "homebrew", "brew upgrade", "installed under a Homebrew prefix", true
	case strings.HasPrefix(p, "/snap/"):
		return "snap", "sudo snap refresh", "installed by snap", true
	case strings.HasPrefix(p, "/nix/store/"):
		return "nix", "", "installed in the nix store", true
	}
	return "", "", "", false
}

// systemPath is where a distribution package may have put the binary; only
// there is the package database asked.
func systemPath(p string) bool {
	for _, prefix := range []string{"/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/libexec/", "/usr/share/", "/bin/", "/sbin/", "/opt/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// queryPackageOwner asks dpkg, rpm and pacman, whichever is installed, which
// package owns path.
func queryPackageOwner(path string) (owner, hint string, ok bool) {
	run := func(name string, args ...string) (string, bool) {
		if _, err := exec.LookPath(name); err != nil {
			return "", false
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).Output()
		if err != nil {
			return "", false
		}
		return strings.TrimSpace(string(out)), true
	}
	if out, ok := run("dpkg", "-S", path); ok {
		// "pkg[:arch]: /path"
		pkg := strings.SplitN(strings.SplitN(out, ":", 2)[0], ",", 2)[0]
		return "dpkg:" + pkg, "sudo apt-get install --only-upgrade " + pkg, true
	}
	if out, ok := run("rpm", "-qf", "--queryformat", "%{NAME}", path); ok && out != "" {
		return "rpm:" + out, "sudo dnf upgrade " + out, true
	}
	if out, ok := run("pacman", "-Qqo", path); ok && out != "" {
		return "pacman:" + out, "sudo pacman -Syu", true
	}
	return "", "", false
}

// probeWritable creates and removes a file in dir: the only honest test that
// a rename there will work (read-only mounts, ACLs and immutable bits all
// show up here and nowhere cheaper).
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".selfupdate-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
