package debug

import "time"

// SessionInfo is persisted to .forge/debug-session.json so subsequent
// debug commands can reconnect to the same debugger.
type SessionInfo struct {
	Type   string `json:"type"`    // "delve"
	Addr   string `json:"addr"`    // e.g. "127.0.0.1:2345"
	PID    int    `json:"pid"`     // debugged (target) process PID
	DlvPID int    `json:"dlv_pid"` // the dlv server process PID forge spawned (0 if unknown)
	Binary string `json:"binary"`  // binary path or service name
	TmpDir string `json:"tmp_dir"` // temp build directory for cleanup
	Docker bool   `json:"docker"`  // true if session is running in a Docker container
	// Owned reports whether forge launched the target process itself
	// (`forge debug start <service>` / `--docker`). When false the session
	// is an ATTACH (`forge debug --attach <pid>`) onto a process forge does
	// not own: `stop` MUST detach the debugger and leave the target alive.
	// Killing an attached target is a data-loss-class bug — it terminated a
	// live admin-server in the field.
	Owned   bool      `json:"owned"`
	Started time.Time `json:"started"`
}

// StopState describes where the debugger stopped after a continue/step.
type StopState struct {
	File        string     `json:"file"`
	Line        int        `json:"line"`
	Function    string     `json:"function"`
	Reason      string     `json:"reason"` // "breakpoint", "step", "next", etc.
	GoroutineID int64      `json:"goroutine_id"`
	Args        []Variable `json:"args,omitempty"`
	Locals      []Variable `json:"locals,omitempty"`
}

// Variable represents an inspectable variable with optional children.
type Variable struct {
	Name     string     `json:"name"`
	Type     string     `json:"type"`
	Value    string     `json:"value"`
	Children []Variable `json:"children,omitempty"`
}

// BreakpointInfo describes a breakpoint.
type BreakpointInfo struct {
	ID           int    `json:"id"`
	File         string `json:"file"`
	Line         int    `json:"line"`
	FunctionName string `json:"function_name,omitempty"`
	Condition    string `json:"condition,omitempty"`
	HitCount     uint64 `json:"hit_count"`
}

// StackFrame describes one frame in a stack trace.
type StackFrame struct {
	Function string     `json:"function"`
	File     string     `json:"file"`
	Line     int        `json:"line"`
	Args     []Variable `json:"args,omitempty"`
	Locals   []Variable `json:"locals,omitempty"`
}

// GoroutineInfo describes a goroutine.
type GoroutineInfo struct {
	ID          int64  `json:"id"`
	Status      string `json:"status"`
	Function    string `json:"function"`
	CurrentFile string `json:"current_file"`
	CurrentLine int    `json:"current_line"`
}
