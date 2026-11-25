// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"bytes"
	"encoding/json"
	"math"

	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// v1.29.0 shipped two workflow keys that were later removed: a workflow's
// `requires.cli_login` (vendor agent CLI sign-ins checked before the first
// node) and a run node's `llm_gateway` (a step key for a third-party agent
// CLI). A definition that still declares either no longer loads: the load
// activity's parse refuses it. A run recorded while they existed carries them
// in its history, though, and must replay as it was recorded. This file is
// what keeps it replaying.

// cliLoginRemovedChangeID gates the removal of requires.cli_login. A root run
// recorded while it existed, whose definition declared it, counted its
// sign-ins as a requirement on the machine: it scheduled PreflightRequirements
// at startup (and on each placed pool member) even when it required nothing
// else there. Such a history has no marker and keeps that schedule; a run
// that reaches this point live records the marker and checks tools, repos
// and integrations only. Asked only when the recorded definition declared
// cli_login, so no other run records it.
const cliLoginRemovedChangeID = "requires-cli-login-removed"

// legacyRequiresCLILogin returns the requires.cli_login a root run recorded
// before the removal declared, or nil: for a child (its root checked), for a
// definition without it, and for a run that recorded cliLoginRemovedChangeID.
// workflowJSON is the definition ActivityLoadWorkflow returned, read from the
// run's history on replay.
func legacyRequiresCLILogin(ctx workflow.Context, workflowJSON []byte, execCtx *ExecutionContext) []string {
	if execCtx != nil && execCtx.Parent != nil {
		return nil
	}
	names := recordedCLILogin(workflowJSON)
	if len(names) == 0 {
		return nil
	}
	if workflow.GetVersion(ctx, cliLoginRemovedChangeID, workflow.DefaultVersion, 1) != workflow.DefaultVersion {
		return nil
	}
	return names
}

// recordedCLILogin reads requires.cli_login from a definition's protojson
// (field 4 of WorkflowRequirements, now reserved, so the generated type no
// longer decodes it). protojson writes the JSON name; the proto name is
// accepted as well.
func recordedCLILogin(workflowJSON []byte) []string {
	if len(workflowJSON) == 0 {
		return nil
	}
	var def struct {
		Requires struct {
			CliLogin      []string `json:"cliLogin"`
			CliLoginProto []string `json:"cli_login"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(workflowJSON, &def); err != nil {
		return nil
	}
	if len(def.Requires.CliLogin) > 0 {
		return def.Requires.CliLogin
	}
	return def.Requires.CliLoginProto
}

// dropRemovedDefinitionKeys deletes the removed keys from a raw definition
// recorded before their removal, so its replayed template resolution parses
// as it did then: requires.cli_login, and llm_gateway from every run node
// (the graph's, and those nested in loops, inline workflows and handlers),
// whether written in its args or, as the load activity re-marshals a
// definition, on the node itself. A definition loaded since the removal never carries them (the
// load activity refuses it first), so this changes nothing for a new run.
func dropRemovedDefinitionKeys(raw map[string]interface{}) {
	if req, ok := raw["requires"].(map[string]interface{}); ok {
		delete(req, "cli_login")
	}
	dropRunLLMGateway(raw)
}

func dropRunLLMGateway(v interface{}) {
	switch t := v.(type) {
	case map[string]interface{}:
		if t["type"] == model.NodeTypeRun {
			delete(t, "llm_gateway")
			if args, ok := t["args"].(map[string]interface{}); ok {
				delete(args, "llm_gateway")
			}
		}
		for _, child := range t {
			dropRunLLMGateway(child)
		}
	case []interface{}:
		for _, child := range t {
			dropRunLLMGateway(child)
		}
	}
}

// removedRunArgsLLMGateway is RunArgs' field number for the removed
// llm_gateway (reserved in workflow_v2.proto).
const removedRunArgsLLMGateway protowire.Number = 7

var runArgsName = (&reliantv1.RunArgs{}).ProtoReflect().Descriptor().FullName()

// markRecordedLLMGateway carries the removed llm_gateway a run recorded
// before its removal declared back onto that run's parsed definition: each
// run node whose recorded protojson (workflowJSON, from the run's history)
// set it gets it back on its RunArgs as an unknown field 7, encoded as the
// LLMGatewayStep it was. The node itself then says so, wherever it sits: an
// id repeats across graph scopes (a loop body, an inline workflow, a
// handler), a node does not. startRun passes it to ExecuteRunStep, which
// refuses the command rather than run it without the step key it was
// declared with. wf and the recorded JSON are the same definition, so their
// shapes match; anywhere they do not, nothing is marked. A definition loaded
// since the removal never carries the key, so this changes nothing for it.
func markRecordedLLMGateway(wf *reliantv1.Workflow, workflowJSON []byte) {
	if wf == nil || (!bytes.Contains(workflowJSON, []byte("llmGateway")) && !bytes.Contains(workflowJSON, []byte("llm_gateway"))) {
		return
	}
	var def map[string]interface{}
	if err := json.Unmarshal(workflowJSON, &def); err != nil {
		return
	}
	markRecordedLLMGatewayIn(wf.ProtoReflect(), def)
}

func markRecordedLLMGatewayIn(m protoreflect.Message, recorded map[string]interface{}) {
	if m.Descriptor().FullName() == runArgsName {
		gw, ok := recorded["llmGateway"].(map[string]interface{})
		if !ok {
			gw, ok = recorded["llm_gateway"].(map[string]interface{})
		}
		if ok {
			m.SetUnknown(append(m.GetUnknown(), encodeRemovedLLMGateway(gw)...))
		}
	}
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Message() == nil || !m.Has(fd) {
			continue
		}
		v, ok := recorded[fd.JSONName()]
		if !ok {
			v, ok = recorded[string(fd.Name())]
		}
		if !ok {
			continue
		}
		switch {
		case fd.IsMap():
			obj, isObj := v.(map[string]interface{})
			if fd.MapValue().Message() == nil || !isObj {
				continue
			}
			m.Get(fd).Map().Range(func(k protoreflect.MapKey, val protoreflect.Value) bool {
				if sub, ok := obj[k.String()].(map[string]interface{}); ok {
					markRecordedLLMGatewayIn(val.Message(), sub)
				}
				return true
			})
		case fd.IsList():
			arr, isArr := v.([]interface{})
			list := m.Get(fd).List()
			if !isArr || len(arr) != list.Len() {
				continue
			}
			for k := range arr {
				if sub, ok := arr[k].(map[string]interface{}); ok {
					markRecordedLLMGatewayIn(list.Get(k).Message(), sub)
				}
			}
		default:
			if sub, ok := v.(map[string]interface{}); ok {
				markRecordedLLMGatewayIn(m.Get(fd).Message(), sub)
			}
		}
	}
}

// encodeRemovedLLMGateway is field 7 of RunArgs holding the recorded
// LLMGatewayStep {cli = 1, budget_usd = 2, models = 3}, wire-compatible with
// a reader that still knows it.
func encodeRemovedLLMGateway(gw map[string]interface{}) []byte {
	var step []byte
	if cli, ok := gw["cli"].(string); ok {
		step = protowire.AppendTag(step, 1, protowire.BytesType)
		step = protowire.AppendString(step, cli)
	}
	budget, ok := gw["budgetUsd"].(float64)
	if !ok {
		budget, ok = gw["budget_usd"].(float64)
	}
	if ok {
		step = protowire.AppendTag(step, 2, protowire.Fixed64Type)
		step = protowire.AppendFixed64(step, math.Float64bits(budget))
	}
	if models, ok := gw["models"].([]interface{}); ok {
		for _, model := range models {
			if name, ok := model.(string); ok {
				step = protowire.AppendTag(step, 3, protowire.BytesType)
				step = protowire.AppendString(step, name)
			}
		}
	}
	b := protowire.AppendTag(nil, removedRunArgsLLMGateway, protowire.BytesType)
	return protowire.AppendBytes(b, step)
}

// removedLLMGatewayOf is the removed llm_gateway markRecordedLLMGateway put
// back on run, as JSON for ExecuteRunStep's input, if any.
func removedLLMGatewayOf(run *reliantv1.RunArgs) (json.RawMessage, bool) {
	if run == nil {
		return nil, false
	}
	for b := run.ProtoReflect().GetUnknown(); len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, false
		}
		b = b[n:]
		if num == removedRunArgsLLMGateway && typ == protowire.BytesType {
			step, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, false
			}
			return decodeRemovedLLMGateway(step), true
		}
		m := protowire.ConsumeFieldValue(num, typ, b)
		if m < 0 {
			return nil, false
		}
		b = b[m:]
	}
	return nil, false
}

// decodeRemovedLLMGateway is an LLMGatewayStep's bytes as the JSON its run
// node was written with ({cli, budget_usd, models}).
func decodeRemovedLLMGateway(step []byte) json.RawMessage {
	gw := struct {
		CLI       string   `json:"cli,omitempty"`
		BudgetUSD float64  `json:"budget_usd,omitempty"`
		Models    []string `json:"models,omitempty"`
	}{}
	for len(step) > 0 {
		num, typ, n := protowire.ConsumeTag(step)
		if n < 0 {
			break
		}
		step = step[n:]
		switch {
		case num == 1 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(step)
			if m < 0 {
				return json.RawMessage(`{}`)
			}
			gw.CLI, step = v, step[m:]
		case num == 2 && typ == protowire.Fixed64Type:
			v, m := protowire.ConsumeFixed64(step)
			if m < 0 {
				return json.RawMessage(`{}`)
			}
			gw.BudgetUSD, step = math.Float64frombits(v), step[m:]
		case num == 3 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(step)
			if m < 0 {
				return json.RawMessage(`{}`)
			}
			gw.Models, step = append(gw.Models, v), step[m:]
		default:
			m := protowire.ConsumeFieldValue(num, typ, step)
			if m < 0 {
				return json.RawMessage(`{}`)
			}
			step = step[m:]
		}
	}
	raw, err := json.Marshal(gw)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
