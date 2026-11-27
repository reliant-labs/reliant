package core

import (
	"encoding/json"
	"errors"
	"time"
)

// The owner's capability grants to a workflow activation on a machine pool
// (pipeline_capability_grants; db/migrations/20261127151550_pipeline_capability_grants.up.sql),
// decided by internal/pipeline/grants.

// PipelineCapability is a power a grant gives an activation's runs.
type PipelineCapability string

const (
	// PipelineCapabilityApprove: a model review verdict the run writes
	// counts (pr.verdict@1 records a pass).
	PipelineCapabilityApprove PipelineCapability = "approve"
	// PipelineCapabilityLand: the merge queue may squash-merge a head the
	// run's verdict admitted.
	PipelineCapabilityLand PipelineCapability = "land"
)

// AllPipelineCapabilities is every PipelineCapability
// (pipeline_capability_grants_capability).
var AllPipelineCapabilities = []PipelineCapability{
	PipelineCapabilityApprove,
	PipelineCapabilityLand,
}

// ErrCapabilityGrantNotFound is a grant that does not exist, or that is not
// the caller's.
var ErrCapabilityGrantNotFound = errors.New("pipeline: capability grant not found")

// PipelineCapabilityGrant is one grant (pipeline_capability_grants).
type PipelineCapabilityGrant struct {
	ID string
	// UserID is the owner who granted it; OrgID the activation's org, "" for
	// none.
	UserID, OrgID string
	// TriggerID is the activation; MachinePoolID the pool its runs must run
	// on; RepoID the enrolled repository it is limited to, "" for any.
	TriggerID, MachinePoolID, RepoID string
	Capability                       PipelineCapability
	// ApprovalID is the card whose approval made it.
	ApprovalID string
	GrantedAt  time.Time
	// RevokedAt is nil while the grant is live; RevokedByUserID who revoked
	// it.
	RevokedAt       *time.Time
	RevokedByUserID string
}

// Live reports whether the grant is in force (not revoked).
func (g *PipelineCapabilityGrant) Live() bool { return g != nil && g.RevokedAt == nil }

// PipelineCapabilityGrantNew is a grant to write.
type PipelineCapabilityGrantNew struct {
	UserID, OrgID                    string
	TriggerID, MachinePoolID, RepoID string
	Capability                       PipelineCapability
	ApprovalID                       string
}

// PipelineGrantFacts is what a grant is checked against when it is written
// and on every use: the owners of the activation and of the pool, the pool's
// selector, and the machine's owner and durable labels. A field whose record
// is missing is empty (its Found flag false).
type PipelineGrantFacts struct {
	TriggerFound  bool
	TriggerName   string
	TriggerUserID string
	TriggerOrgID  string
	// TriggerPoolID is the pool the activation runs on today, "" for none.
	TriggerPoolID string

	PoolFound    bool
	PoolUserID   string
	PoolName     string
	PoolSelector map[string]string
}

// PipelineDaemonFacts is a machine's owner and durable labels, as its owner
// set them (daemons.labels): never the labels it reports when it connects.
type PipelineDaemonFacts struct {
	Found  bool
	UserID string
	Labels map[string]string
}

// The approval card that asks the owner for capability grants: an
// APPROVAL_TYPE_TOOL_CALL approval whose metadata names this kind.
const (
	// ApprovalMetadataKind is the metadata key naming what an approval is
	// for, when it is not a plain tool call or workflow step.
	ApprovalMetadataKind = "kind"
	// ApprovalKindPipelineCapabilityGrant is a request for capability
	// grants (CapabilityGrantRequest).
	ApprovalKindPipelineCapabilityGrant = "pipeline_capability_grant"
)

// CapabilityGrantRequest is what a capability-grant card asks the owner to
// grant: capabilities to an activation, on a pool, optionally for one
// enrolled repository.
type CapabilityGrantRequest struct {
	Kind          string               `json:"kind"`
	TriggerID     string               `json:"trigger_id"`
	MachinePoolID string               `json:"machine_pool_id"`
	RepoID        string               `json:"repo_id,omitempty"`
	Capabilities  []PipelineCapability `json:"capabilities"`
	// RequestedBy is the run that asked, for the audit trail.
	RequestedByRunID string `json:"requested_by_run_id,omitempty"`
}

// CapabilityGrantRequest returns the grant request a card carries, or nil
// when the approval is not one.
func (a *Approval) CapabilityGrantRequest() *CapabilityGrantRequest {
	if a == nil || a.Metadata == nil {
		return nil
	}
	var req CapabilityGrantRequest
	if json.Unmarshal([]byte(*a.Metadata), &req) != nil || req.Kind != ApprovalKindPipelineCapabilityGrant {
		return nil
	}
	return &req
}
