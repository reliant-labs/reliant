package osutil

import (
	"encoding/json"
	"fmt"
	"os"
)

// CommandSupervision is a private supervisor result channel, never payload
// stdout/stderr or exit status. Finish is called only after cmd.Wait.
type CommandSupervision struct {
	file     *os.File
	writer   *os.File
	fallback func() error
}
type supervisorResult struct {
	Complete         bool   `json:"complete"`
	OutputIncomplete bool   `json:"output_incomplete"`
	Error            string `json:"error,omitempty"`
}

func (s *CommandSupervision) Close() {
	if s != nil && s.writer != nil {
		_ = s.writer.Close()
	}
	if s != nil && s.file != nil {
		_ = s.file.Close()
	}
}
func (s *CommandSupervision) Finish() (bool, error) {
	if s.writer != nil {
		_ = s.writer.Close()
	}
	if s.fallback != nil {
		return false, s.fallback()
	}
	var b [4096]byte
	n, err := s.file.ReadAt(b[:], 0)
	if n == 0 {
		return false, fmt.Errorf("command supervisor result unavailable: %v", err)
	}
	var result supervisorResult
	if json.Unmarshal(b[:n], &result) != nil {
		return false, fmt.Errorf("invalid command supervisor result")
	}
	if result.Error != "" {
		return result.OutputIncomplete, fmt.Errorf("command shutdown unconfirmed: %s", result.Error)
	}
	if !result.Complete {
		return result.OutputIncomplete, fmt.Errorf("command supervisor did not confirm complete tree exit")
	}
	return result.OutputIncomplete, nil
}
