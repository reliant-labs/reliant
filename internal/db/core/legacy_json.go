package core

import (
	"database/sql"
	"encoding/json"
	"time"
)

// ─── the pre-pointer JSON shape of the step and scenario models ────────────
//
// StepExecution, ChatStepExecution and WorkflowScenario held their nullable
// columns as sql.Null* structs until they became pointers. encoding/json
// renders a sql.Null* as {"String":"x","Valid":true} — and never omits it,
// omitempty or not — where a pointer renders as the bare value or null. So
// that the change of field type can never change a payload, whichever path
// one of these values takes to an encoder (directly, inside another struct,
// or behind an interface), each model marshals and unmarshals through a
// mirror of its former declaration. legacy_json_test.go pins the bytes
// against verbatim copies of those declarations.

type legacyStepExecution struct {
	ID             string         `json:"id"`
	WorkflowID     string         `json:"workflow_id"`
	StepID         string         `json:"step_id"`
	ActivityName   string         `json:"activity_name"`
	OutputJSON     sql.NullString `json:"output_json"`
	ExitCode       sql.NullInt64  `json:"exit_code"`
	Success        sql.NullBool   `json:"success"`
	DurationMs     sql.NullInt64  `json:"duration_ms"`
	LoopNodeID     sql.NullString `json:"loop_node_id"`
	LoopIteration  sql.NullInt64  `json:"loop_iteration"`
	CreatedAt      time.Time      `json:"created_at"`
	SavedMessageID sql.NullString `json:"saved_message_id"`
	InputJSON      sql.NullString `json:"input_json"`
	ErrorMessage   sql.NullString `json:"error_message"`
	Attempt        sql.NullInt32  `json:"attempt"`
	NodePath       sql.NullString `json:"node_path"`
}

// MarshalJSON renders the pre-pointer sql.Null* shape (see above).
func (s StepExecution) MarshalJSON() ([]byte, error) {
	return json.Marshal(legacyStepExecution{
		ID:             s.ID,
		WorkflowID:     s.WorkflowID,
		StepID:         s.StepID,
		ActivityName:   s.ActivityName,
		OutputJSON:     legacyString(s.OutputJSON),
		ExitCode:       legacyInt64(s.ExitCode),
		Success:        legacyBool(s.Success),
		DurationMs:     legacyInt64(s.DurationMs),
		LoopNodeID:     legacyString(s.LoopNodeID),
		LoopIteration:  legacyInt64(s.LoopIteration),
		CreatedAt:      s.CreatedAt,
		SavedMessageID: legacyString(s.SavedMessageID),
		InputJSON:      legacyString(s.InputJSON),
		ErrorMessage:   legacyString(s.ErrorMessage),
		Attempt:        legacyInt32(s.Attempt),
		NodePath:       legacyString(s.NodePath),
	})
}

// UnmarshalJSON reads the shape MarshalJSON writes.
func (s *StepExecution) UnmarshalJSON(data []byte) error {
	var l legacyStepExecution
	if err := json.Unmarshal(data, &l); err != nil {
		return err
	}
	*s = StepExecution{
		ID:             l.ID,
		WorkflowID:     l.WorkflowID,
		StepID:         l.StepID,
		ActivityName:   l.ActivityName,
		OutputJSON:     ptrString(l.OutputJSON),
		ExitCode:       ptrInt64(l.ExitCode),
		Success:        ptrBool(l.Success),
		DurationMs:     ptrInt64(l.DurationMs),
		LoopNodeID:     ptrString(l.LoopNodeID),
		LoopIteration:  ptrInt64(l.LoopIteration),
		CreatedAt:      l.CreatedAt,
		SavedMessageID: ptrString(l.SavedMessageID),
		InputJSON:      ptrString(l.InputJSON),
		ErrorMessage:   ptrString(l.ErrorMessage),
		Attempt:        ptrInt32(l.Attempt),
		NodePath:       ptrString(l.NodePath),
	}
	return nil
}

type legacyChatStepExecution struct {
	ID             string
	WorkflowID     string
	StepID         string
	ActivityName   string
	ExitCode       sql.NullInt64
	Success        sql.NullBool
	DurationMs     sql.NullInt64
	LoopNodeID     sql.NullString
	LoopIteration  sql.NullInt64
	CreatedAt      time.Time
	SavedMessageID sql.NullString
	OutputJSON     string
}

// MarshalJSON renders the pre-pointer sql.Null* shape (see above).
func (c ChatStepExecution) MarshalJSON() ([]byte, error) {
	return json.Marshal(legacyChatStepExecution{
		ID:             c.ID,
		WorkflowID:     c.WorkflowID,
		StepID:         c.StepID,
		ActivityName:   c.ActivityName,
		ExitCode:       legacyInt64(c.ExitCode),
		Success:        legacyBool(c.Success),
		DurationMs:     legacyInt64(c.DurationMs),
		LoopNodeID:     legacyString(c.LoopNodeID),
		LoopIteration:  legacyInt64(c.LoopIteration),
		CreatedAt:      c.CreatedAt,
		SavedMessageID: legacyString(c.SavedMessageID),
		OutputJSON:     c.OutputJSON,
	})
}

// UnmarshalJSON reads the shape MarshalJSON writes.
func (c *ChatStepExecution) UnmarshalJSON(data []byte) error {
	var l legacyChatStepExecution
	if err := json.Unmarshal(data, &l); err != nil {
		return err
	}
	*c = ChatStepExecution{
		ID:             l.ID,
		WorkflowID:     l.WorkflowID,
		StepID:         l.StepID,
		ActivityName:   l.ActivityName,
		ExitCode:       ptrInt64(l.ExitCode),
		Success:        ptrBool(l.Success),
		DurationMs:     ptrInt64(l.DurationMs),
		LoopNodeID:     ptrString(l.LoopNodeID),
		LoopIteration:  ptrInt64(l.LoopIteration),
		CreatedAt:      l.CreatedAt,
		SavedMessageID: ptrString(l.SavedMessageID),
		OutputJSON:     l.OutputJSON,
	}
	return nil
}

type legacyWorkflowScenario struct {
	ID              string         `json:"id"`
	WorkflowDraftID sql.NullString `json:"workflow_draft_id,omitempty"`
	UserID          string         `json:"user_id"`
	Name            string         `json:"name"`
	Description     sql.NullString `json:"description,omitempty"`
	Events          string         `json:"events"`
	Expect          sql.NullString `json:"expect,omitempty"`
	LastRunAt       sql.NullTime   `json:"last_run_at,omitempty"`
	LastRunStatus   sql.NullString `json:"last_run_status,omitempty"`
	LastRunResult   sql.NullString `json:"last_run_result,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	Version         int64          `json:"version"`
}

// MarshalJSON renders the pre-pointer sql.Null* shape (see above).
func (w WorkflowScenario) MarshalJSON() ([]byte, error) {
	return json.Marshal(legacyWorkflowScenario{
		ID:              w.ID,
		WorkflowDraftID: legacyString(w.WorkflowDraftID),
		UserID:          w.UserID,
		Name:            w.Name,
		Description:     legacyString(w.Description),
		Events:          w.Events,
		Expect:          legacyString(w.Expect),
		LastRunAt:       legacyTime(w.LastRunAt),
		LastRunStatus:   legacyString(w.LastRunStatus),
		LastRunResult:   legacyString(w.LastRunResult),
		CreatedAt:       w.CreatedAt,
		UpdatedAt:       w.UpdatedAt,
		Version:         w.Version,
	})
}

// UnmarshalJSON reads the shape MarshalJSON writes.
func (w *WorkflowScenario) UnmarshalJSON(data []byte) error {
	var l legacyWorkflowScenario
	if err := json.Unmarshal(data, &l); err != nil {
		return err
	}
	*w = WorkflowScenario{
		ID:              l.ID,
		WorkflowDraftID: ptrString(l.WorkflowDraftID),
		UserID:          l.UserID,
		Name:            l.Name,
		Description:     ptrString(l.Description),
		Events:          l.Events,
		Expect:          ptrString(l.Expect),
		LastRunAt:       ptrTime(l.LastRunAt),
		LastRunStatus:   ptrString(l.LastRunStatus),
		LastRunResult:   ptrString(l.LastRunResult),
		CreatedAt:       l.CreatedAt,
		UpdatedAt:       l.UpdatedAt,
		Version:         l.Version,
	}
	return nil
}

func legacyString(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func legacyInt64(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func legacyInt32(p *int32) sql.NullInt32 {
	if p == nil {
		return sql.NullInt32{}
	}
	return sql.NullInt32{Int32: *p, Valid: true}
}

func legacyBool(p *bool) sql.NullBool {
	if p == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *p, Valid: true}
}

func legacyTime(p *time.Time) sql.NullTime {
	if p == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *p, Valid: true}
}

func ptrString(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	return &n.String
}

func ptrInt64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func ptrInt32(n sql.NullInt32) *int32 {
	if !n.Valid {
		return nil
	}
	return &n.Int32
}

func ptrBool(n sql.NullBool) *bool {
	if !n.Valid {
		return nil
	}
	return &n.Bool
}

func ptrTime(n sql.NullTime) *time.Time {
	if !n.Valid {
		return nil
	}
	return &n.Time
}
