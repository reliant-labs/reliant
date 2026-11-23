package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/reliant-labs/forge/internal/storage"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// Task Scheduler folder + name; the folder keeps forge's task out of the
// root namespace.
const (
	windowsTaskName       = `\Reliant\forge-storage`
	windowsHourlyTaskName = `\Reliant\forge-storage-hourly`
)

// windowsTaskMarkerPath is where installWindowsTask keeps a copy of the task
// XML. storageScheduleInstalled stats this file instead of spawning schtasks
// on every `forge env up`, mirroring the plist/timer file check on the other
// platforms.
func windowsTaskMarkerPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "forge", "forge-storage.task.xml"), nil
}

// escapeWindowsArg quotes one argument by the CommandLineToArgvW rules (the
// same algorithm as syscall.EscapeArg, which only exists on windows).
func escapeWindowsArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	backslashes := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			backslashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, backslashes*2+1))
			b.WriteByte('"')
			backslashes = 0
		default:
			b.WriteString(strings.Repeat(`\`, backslashes))
			backslashes = 0
			b.WriteByte(c)
		}
	}
	b.WriteString(strings.Repeat(`\`, backslashes*2))
	b.WriteByte('"')
	return b.String()
}

func joinWindowsArgs(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = escapeWindowsArg(a)
	}
	return strings.Join(quoted, " ")
}

// windowsTaskXML builds the Task Scheduler definition: daily at 03:30 (first
// boundary is start's date), run-if-missed, 30 minute cap, never elevated.
// The task inherits the user's environment (InteractiveToken runs with the
// user's profile), so unlike launchd/systemd no PATH is baked in: Task
// Scheduler has no per-task env block and a `cmd /c set PATH=...` wrapper
// would add a quoting layer for no gain. Output is not captured either, and
// forge storage gc keeps no log file of its own.
func windowsTaskXML(command string, args []string, start time.Time) []byte {
	return windowsTaskXMLFor(command, args, start, false)
}

// windowsTaskXMLFor builds the daily task, or with hourly the bounded cache
// pass: a time trigger repeating every hour, 20 minute cap.
func windowsTaskXMLFor(command string, args []string, start time.Time, hourly bool) []byte {
	esc := func(s string) string { var b strings.Builder; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	boundary := time.Date(start.Year(), start.Month(), start.Day(), 3, 30, 0, 0, time.UTC).Format("2006-01-02T15:04:05")
	trigger := `<CalendarTrigger>
      <StartBoundary>` + boundary + `</StartBoundary>
      <Enabled>true</Enabled>
      <ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay>
    </CalendarTrigger>`
	description, limit := "Daily Forge storage maintenance", "PT30M"
	if hourly {
		trigger = `<TimeTrigger>
      <Repetition><Interval>PT1H</Interval></Repetition>
      <StartBoundary>` + start.UTC().Format("2006-01-02T15:04:05") + `</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>`
		description, limit = "Hourly Forge cache maintenance", "PT20M"
	}
	return []byte(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>` + description + `</Description></RegistrationInfo>
  <Triggers>
    ` + trigger + `
  </Triggers>
  <Principals>
    <Principal id="Author"><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>` + limit + `</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>` + esc(command) + `</Command><Arguments>` + esc(joinWindowsArgs(args)) + `</Arguments></Exec>
  </Actions>
</Task>
`)
}

// encodeUTF16LEBOM is the encoding schtasks /XML reliably accepts: the file
// declares encoding="UTF-16" and Microsoft's own exports are UTF-16 LE with a
// BOM; UTF-8 files are rejected by some builds ("unable to switch the
// encoding", see steipete/summarize#315).
func encodeUTF16LEBOM(b []byte) []byte {
	units := utf16.Encode([]rune(string(b)))
	out := bytes.NewBuffer(make([]byte, 0, 2+2*len(units)))
	out.Write([]byte{0xFF, 0xFE})
	for _, u := range units {
		out.WriteByte(byte(u))
		out.WriteByte(byte(u >> 8))
	}
	return out.Bytes()
}

func installWindowsTask(ctx context.Context, executable string, args []string, hourly bool) error {
	data := encodeUTF16LEBOM(windowsTaskXMLFor(executable, args, time.Now(), hourly))
	marker, err := windowsTaskMarkerPath()
	name := windowsTaskName
	if hourly {
		name = windowsHourlyTaskName
		marker = strings.TrimSuffix(marker, ".task.xml") + "-hourly.task.xml"
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return err
	}
	if err := atomicfile.WriteFile(marker, data, 0600); err != nil {
		return err
	}
	if _, err := storage.Exec(ctx, "schtasks", "/Create", "/TN", name, "/XML", marker, "/F"); err != nil {
		_ = os.Remove(marker)
		return fmt.Errorf("register scheduled task: %w", err)
	}
	return nil
}
