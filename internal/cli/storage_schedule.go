package cli

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/hostlaunch"
	"github.com/reliant-labs/forge/internal/storage"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// storageScheduleInstalledFn is the seam the notice reads through.
var storageScheduleInstalledFn = storageScheduleInstalled

// storageScheduleInstalled reports whether installStorageSchedule has run on
// this machine, by the presence of the unit file it writes. Checking the FILE
// rather than asking launchctl/systemctl keeps this cheap enough to call on
// every `forge env up` and keeps it from printing an error of its own on a
// platform where neither manager exists.
//
// An unreadable home directory, or any OS forge cannot schedule on, reports
// true: the notice's only purpose is to tell someone to run a command, and on a
// platform where that command prints "schedule it yourself" the notice is
// noise.
func storageScheduleInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return true
	}
	switch runtime.GOOS {
	case "darwin":
		_, err = os.Stat(filepath.Join(home, "Library", "LaunchAgents", "com.reliant.forge-storage.plist"))
	case "linux":
		_, err = os.Stat(filepath.Join(home, ".config", "systemd", "user", "forge-storage.timer"))
	case "windows":
		// Task Scheduler has no unit file, so installWindowsTask leaves a copy
		// of the task XML under the user config dir for this check to find.
		marker, mErr := windowsTaskMarkerPath()
		if mErr != nil {
			return true
		}
		_, err = os.Stat(marker)
	default:
		return true
	}
	return err == nil
}

func installStorageSchedule(ctx context.Context, out io.Writer, path string, p storage.Policy) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return fmt.Errorf("on %s schedule 'forge storage daemon --policy %s' with your service manager", runtime.GOOS, path)
	}
	r := storage.Runner{Policy: p}
	if err := r.Local(ctx); err != nil {
		return err
	}
	if p.DockerContext == "" {
		b, err := storage.Exec(ctx, "docker", "context", "show")
		if err != nil {
			return err
		}
		p.DockerContext = strings.TrimSpace(string(b))
	}
	if err := storage.Save(path, p); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(path), "bin")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tokens, err := forgeExecCommand()
	if err != nil {
		return err
	}
	src, err := os.Open(tokens[0])
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	dst, err := os.CreateTemp(dir, ".forge-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(dst.Name()) }()
	if _, err = io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Chmod(0700); err != nil {
		_ = dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	executable := filepath.Join(dir, hostlaunch.ExeName(runtime.GOOS, "forge-storage"))
	if err := os.Rename(dst.Name(), executable); err != nil {
		if runtime.GOOS == "windows" {
			// Windows cannot replace an .exe that is currently executing.
			return fmt.Errorf("could not replace %s: the storage maintenance task is probably running right now; retry in a few minutes: %w", executable, err)
		}
		return err
	}
	args := dailyStorageArgs(executable, tokens[1:], path)
	// The hourly job is the bounded, non-disruptive pass (`auto-gc`): Go build
	// cache growth under agent load (~40 GB/h) outruns a once-a-day trim. It
	// takes the same maintenance lock as the daily full pass, so they never
	// overlap, and it never touches the registry.
	hourly := []string{executable}
	hourly = append(hourly, tokens[1:]...)
	hourly = append(hourly, "storage", "auto-gc", "--policy", path)
	switch runtime.GOOS {
	case "darwin":
		err = installLaunchAgent(ctx, args, home, dailyLaunchdSchedule)
		if err == nil {
			err = installLaunchAgent(ctx, hourly, home, hourlyLaunchdSchedule)
		}
	case "windows":
		err = installWindowsTask(ctx, executable, args[1:], false)
		if err == nil {
			err = installWindowsTask(ctx, executable, hourly[1:], true)
		}
	default:
		err = installSystemdTimer(ctx, args, home, dailySystemdSchedule)
		if err == nil {
			err = installSystemdTimer(ctx, hourly, home, hourlySystemdSchedule)
		}
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Installed daily storage maintenance at 03:30 and an hourly bounded cache pass using %s; policy %s. Registry cleanup (daily only) briefly interrupts pulls/pushes.\n", executable, path)
	return nil
}

// dailyStorageArgs is the daily job's argv: the full pass, applied, marked
// --scheduled so it never removes worktrees. A timer firing at 03:30 is not
// someone deciding that an idle-looking worktree is abandoned.
func dailyStorageArgs(executable string, prefix []string, policyPath string) []string {
	args := append([]string{executable}, prefix...)
	return append(args, "storage", "gc", "--policy", policyPath, "--apply", "--scheduled")
}

// schedule is one installed job: launchd label / systemd unit stem plus when it fires.
type launchdSchedule struct{ label, trigger string }
type systemdSchedule struct{ unit, description, timer string }

var (
	dailyLaunchdSchedule  = launchdSchedule{"com.reliant.forge-storage", "<key>StartCalendarInterval</key><dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>30</integer></dict>"}
	hourlyLaunchdSchedule = launchdSchedule{"com.reliant.forge-storage-hourly", "<key>StartInterval</key><integer>3600</integer>"}

	dailySystemdSchedule  = systemdSchedule{"forge-storage", "Daily Forge storage maintenance", "OnCalendar=*-*-* 03:30:00\nPersistent=true\n"}
	hourlySystemdSchedule = systemdSchedule{"forge-storage-hourly", "Hourly Forge cache maintenance", "OnCalendar=hourly\nPersistent=true\n"}
)

func installLaunchAgent(ctx context.Context, args []string, home string, sched launchdSchedule) error {
	var err error

	var arguments strings.Builder
	for _, arg := range args {
		arguments.WriteString("<string>")
		_ = xml.EscapeText(&arguments, []byte(arg))
		arguments.WriteString("</string>")
	}
	logs := filepath.Join(home, "Library", "Logs", "forge-storage")
	if err := os.MkdirAll(logs, 0700); err != nil {
		return err
	}
	escape := func(s string) string { var b strings.Builder; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array>%s</array>%s<key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string><key>ExitTimeOut</key><integer>120</integer></dict></plist>`, sched.label, arguments.String(), sched.trigger, escape(os.Getenv("PATH")), escape(filepath.Join(logs, sched.label+".log")), escape(filepath.Join(logs, sched.label+".err.log")))
	file := filepath.Join(home, "Library", "LaunchAgents", sched.label+".plist")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(file, []byte(plist), 0600); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_, _ = storage.Exec(ctx, "launchctl", "bootout", domain+"/"+sched.label)
	if _, err = storage.Exec(ctx, "launchctl", "bootstrap", domain, file); err != nil {
		return err
	}
	return nil
}
func installSystemdTimer(ctx context.Context, args []string, home string, sched systemdSchedule) error {
	var err error

	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	var quoted []string
	for _, arg := range args {
		quoted = append(quoted, strconv.Quote(strings.ReplaceAll(arg, "%", "%%")))
	}
	service := "[Unit]\nDescription=" + sched.description + "\n[Service]\nType=oneshot\nTimeoutStartSec=30min\nTimeoutStopSec=120s\nExecStart=" + strings.Join(quoted, " ") + "\nEnvironment=" + strconv.Quote("PATH="+os.Getenv("PATH")) + "\n"
	timer := "[Unit]\nDescription=" + sched.description + "\n[Timer]\n" + sched.timer + "[Install]\nWantedBy=timers.target\n"
	if err := atomicfile.WriteFile(filepath.Join(dir, sched.unit+".service"), []byte(service), 0600); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(dir, sched.unit+".timer"), []byte(timer), 0600); err != nil {
		return err
	}
	if _, err = storage.Exec(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if _, err = storage.Exec(ctx, "systemctl", "--user", "enable", "--now", sched.unit+".timer"); err != nil {
		return err
	}
	return nil
}
