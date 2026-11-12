package cli

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestEscapeWindowsArg(t *testing.T) {
	for in, want := range map[string]string{
		"":                  `""`,
		"plain":             "plain",
		`C:\a\b`:            `C:\a\b`,
		"has space":         `"has space"`,
		`say "hi"`:          `"say \"hi\""`,
		`C:\Program Files\`: `"C:\Program Files\\"`,
		`a\"b`:              `"a\\\"b"`,
	} {
		if got := escapeWindowsArg(in); got != want {
			t.Errorf("escapeWindowsArg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWindowsTaskXML(t *testing.T) {
	data := windowsTaskXML(`C:\Users\a b\forge-storage.exe`, []string{"storage", "gc", "--policy", `C:\p q\"x"`, "--apply"}, time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC))
	var task struct {
		Start    string `xml:"Triggers>CalendarTrigger>StartBoundary"`
		Days     string `xml:"Triggers>CalendarTrigger>ScheduleByDay>DaysInterval"`
		RunLevel string `xml:"Principals>Principal>RunLevel"`
		Missed   string `xml:"Settings>StartWhenAvailable"`
		Limit    string `xml:"Settings>ExecutionTimeLimit"`
		Command  string `xml:"Actions>Exec>Command"`
		Args     string `xml:"Actions>Exec>Arguments"`
	}
	if err := xml.NewDecoder(bytes.NewReader(bytes.Replace(data, []byte(`encoding="UTF-16"`), []byte(`encoding="UTF-8"`), 1))).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Start != "2026-07-04T03:30:00" || task.Days != "1" || task.RunLevel != "LeastPrivilege" || task.Missed != "true" || task.Limit != "PT30M" {
		t.Errorf("unexpected task: %+v", task)
	}
	if task.Command != `C:\Users\a b\forge-storage.exe` {
		t.Errorf("command = %q", task.Command)
	}
	if want := `storage gc --policy "C:\p q\\\"x\"" --apply`; task.Args != want {
		t.Errorf("args = %q, want %q", task.Args, want)
	}
	if strings.Contains(string(data), "HighestAvailable") {
		t.Error("task must not be elevated")
	}
}

func TestEncodeUTF16LEBOM(t *testing.T) {
	got := encodeUTF16LEBOM([]byte("a€"))
	want := []byte{0xFF, 0xFE, 'a', 0, 0xAC, 0x20}
	if !bytes.Equal(got, want) {
		t.Errorf("got % x want % x", got, want)
	}
}

func TestWindowsHourlyTaskXML(t *testing.T) {
	data := windowsTaskXMLFor(`C:\f\forge-storage.exe`, []string{"storage", "auto-gc", "--policy", `C:\p`}, time.Date(2026, 7, 4, 9, 0, 0, 0, time.UTC), true)
	var task struct {
		Interval string `xml:"Triggers>TimeTrigger>Repetition>Interval"`
		Limit    string `xml:"Settings>ExecutionTimeLimit"`
		Args     string `xml:"Actions>Exec>Arguments"`
	}
	if err := xml.NewDecoder(bytes.NewReader(bytes.Replace(data, []byte(`encoding="UTF-16"`), []byte(`encoding="UTF-8"`), 1))).Decode(&task); err != nil {
		t.Fatal(err)
	}
	if task.Interval != "PT1H" || task.Limit != "PT20M" || !strings.Contains(task.Args, "auto-gc") || strings.Contains(task.Args, "--apply") {
		t.Fatalf("unexpected hourly task: %+v", task)
	}
}
