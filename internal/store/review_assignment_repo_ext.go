package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/reliant/internal/reviewpublisher"
)

// ReviewAssignmentExtra is the owned seam for non-column fields.
type ReviewAssignmentExtra struct{}

// ReviewAuthorityStore is private, explicitly constructed SQL authority. It is
// not registered in app composition and exposes no public or model write API.
type ReviewAuthorityStore struct{ DB orm.Context }

// ReviewPolicyConfig is provisioned only by a trusted owner/admin fixture.
// ReviewAdmissionKeyExpression is the fixed private graph admission key. Its
// repository ID comes from the verified normalized GitHub event projection.
const ReviewAdmissionKeyExpression = `"github:" + trigger.payload.resource + ":pr:" + string(trigger.payload.data.pull_request.number)`

// ReviewAdmissionScope matches the existing admission owner/project/workflow hash.
// WorkflowRef must already be canonical (no aliases or mutable display name).
func ReviewAdmissionScope(owner, project, workflowRef string) string {
	raw, _ := json.Marshal([]string{owner, project, workflowRef})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type ReviewPolicyConfig struct {
	ProjectID, WorkflowRef                                  string
	ID, OwnerID                                             string
	RepositoryID, Revision                                  int64
	Enabled                                                 bool
	WorkflowDigest, CompletionNode, CheckName, AuthorizedBy string
	GraphSnapshot                                           json.RawMessage
}

// ReviewAssignmentRequest references an already reserved and bound admission.
// The head/author/run identity is loaded from canonical SQL, not accepted here.
type ReviewAssignmentRequest struct {
	ID, PolicyID, BindingID, AdmissionEventID string
	// ProviderHead is obtained by the private server assignment adapter via a
	// fresh authenticated read. It is never a model/CLI-supplied authorization.
	ProviderHead reviewpublisher.Head
}

// ReviewAuthority is the immutable assignment with current authority projection.
type ReviewAuthority struct {
	ID, PolicyID, BindingID, AdmissionEventID, OwnerID, RepoName string
	Binding                                                      reviewpublisher.Binding
	Author                                                       reviewpublisher.Execution
	Policy                                                       reviewpublisher.Policy
	CurrentGeneration                                            int64
	Cancelled, Superseded                                        bool
	Completion                                                   reviewpublisher.Completion
	GraphSnapshot                                                json.RawMessage
	CompletionNode, EvidenceDigest, ActivityID, CompletionRunID  string
}

// ReviewEvidence records verified exact Temporal history, initially pending.
type ReviewEvidence struct {
	AssignmentID, Digest, ActivityID, RunID string
	Verdict                                 reviewpublisher.Verdict
	Terminal, GraphSucceeded                bool
}

// DigestReviewSnapshot hashes canonical JSON, including every dependency and
// fixed completion schema/node. JSON field order is not authority.
func DigestReviewSnapshot(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", reviewpublisher.ErrUnauthorized
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ProvisionReviewPolicy is private provisioning, not a user authorization check.
// Callers MUST establish owner/admin authority before using this fixture seam.
func (s ReviewAuthorityStore) ProvisionReviewPolicy(ctx context.Context, p ReviewPolicyConfig) error {
	digest, err := DigestReviewSnapshot(p.GraphSnapshot)
	if err != nil || digest != p.WorkflowDigest || p.ID == "" || p.OwnerID == "" || p.ProjectID == "" || p.WorkflowRef == "" || p.AuthorizedBy == "" || p.RepositoryID <= 0 || p.Revision <= 0 || p.CheckName != "ReliantReview" || p.CompletionNode == "" || !validReviewAdmissionSnapshot(p.GraphSnapshot, p.WorkflowRef) {
		return reviewpublisher.ErrUnauthorized
	}
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var old struct{ Revision int64 }
		err := s.DB.Bun().NewRaw(`SELECT revision FROM review_policies WHERE id = ?`, p.ID).Scan(ctx, &old)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && p.Revision <= old.Revision {
			return reviewpublisher.ErrUnauthorized
		}
		_, err = s.DB.Bun().NewRaw(`INSERT INTO review_policies (id,owner_id,repository_id,revision,enabled,workflow_digest,graph_snapshot,completion_node,check_name,authorized_by,project_id,workflow_ref,admission_scope)
   VALUES (?,?,?,?,?,?,?::jsonb,?,?,?,?,?,?) ON CONFLICT (id) DO UPDATE SET revision=EXCLUDED.revision,enabled=EXCLUDED.enabled,
   workflow_digest=EXCLUDED.workflow_digest,graph_snapshot=EXCLUDED.graph_snapshot,completion_node=EXCLUDED.completion_node,check_name=EXCLUDED.check_name,authorized_by=EXCLUDED.authorized_by,updated_at=now()
   WHERE review_policies.owner_id=EXCLUDED.owner_id AND review_policies.repository_id=EXCLUDED.repository_id AND review_policies.project_id=EXCLUDED.project_id AND review_policies.workflow_ref=EXCLUDED.workflow_ref`, p.ID, p.OwnerID, p.RepositoryID, p.Revision, p.Enabled, p.WorkflowDigest, string(p.GraphSnapshot), p.CompletionNode, p.CheckName, p.AuthorizedBy, p.ProjectID, p.WorkflowRef, ReviewAdmissionScope(p.OwnerID, p.ProjectID, p.WorkflowRef)).Exec(ctx)
		return err
	})
}

// ResolveReviewWaitPolicy resolves only current enrolled owner/repo/PR/head
// policy. The wait store pins this ID+revision in its own serializable operation.
// A policy revision change between these separate operations fails subsequent
// publication through the assignment authority checks; no instant remote fence
// is claimed. The wait integration must not register another producer namespace.
func (s ReviewAuthorityStore) ResolveReviewWaitPolicy(ctx context.Context, owner string, repoID, pr int64, head string) (string, int64, error) {
	var row struct {
		ID       string
		Revision int64
	}
	err := s.DB.Bun().NewRaw(`SELECT p.id,p.revision FROM review_policies p JOIN pr_handoff_bindings b ON b.owner_id=p.owner_id AND b.repo_id=p.repository_id WHERE p.enabled AND p.owner_id=? AND p.repository_id=? AND b.pr_number=? AND b.head_sha=?`, owner, repoID, pr, head).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, reviewpublisher.ErrUnauthorized
	}
	return row.ID, row.Revision, err
}

// AssignmentForExecution resolves only an enrolled original reviewer execution.
// CAN successors need explicit verified continuity; never use latest-run fallback.
func (s ReviewAuthorityStore) AssignmentForExecution(ctx context.Context, root, run string) (string, error) {
	var id string
	err := s.DB.Bun().NewRaw(`SELECT id FROM review_assignments WHERE reviewer_root_id=? AND reviewer_run_id=? AND NOT cancelled AND NOT superseded`, root, run).Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", reviewpublisher.ErrUnauthorized
	}
	return id, err
}

// AssignmentSubject resolves the enabled policy-bound handoff subject for a fresh
// server provider read. AssignReview repeats all SQL checks after that read.
func (s ReviewAuthorityStore) AssignmentSubject(ctx context.Context, policyID, bindingID string) (reviewpublisher.Head, error) {
	var row struct {
		RepositoryID, PullNumber int64
		SHA                      string
	}
	err := s.DB.Bun().NewRaw(`SELECT b.repo_id AS repository_id,b.pr_number AS pull_number,b.head_sha AS sha FROM pr_handoff_bindings b JOIN review_policies p ON p.id=? AND p.owner_id=b.owner_id AND p.repository_id=b.repo_id AND p.enabled WHERE b.id=?`, policyID, bindingID).Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) {
		return reviewpublisher.Head{}, reviewpublisher.ErrUnauthorized
	}
	if err != nil {
		return reviewpublisher.Head{}, err
	}
	return reviewpublisher.Head{RepositoryID: row.RepositoryID, PullNumber: row.PullNumber, SHA: row.SHA}, nil
}

// AssignReview reserves only private review authority, not admission capacity.
// All identities are derived from the handoff binding and reserved event inside
// this SERIALIZABLE transaction. A repeated source SHA returns its first assignment.
func (s ReviewAuthorityStore) AssignReview(ctx context.Context, in ReviewAssignmentRequest) (ReviewAuthority, error) {
	var out ReviewAuthority
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var row struct {
			OwnerID, RepoName, AuthorRootID, AuthorRunID, HeadSHA, ReviewerRootID, ReviewerRunID, WorkflowDigest, CompletionNode, CheckName string
			RepositoryID, PRNumber, Revision                                                                                                int64
			GraphSnapshot                                                                                                                   json.RawMessage
		}
		err := s.DB.Bun().NewRaw(`SELECT b.owner_id,b.repo_name,b.root_id AS author_root_id,b.execution_id AS author_run_id,b.head_sha,
   b.repo_id AS repository_id,b.pr_number,p.revision,p.workflow_digest,p.graph_snapshot,p.completion_node,p.check_name,
   e.admission_workflow_id AS reviewer_root_id,e.admission_run_id AS reviewer_run_id
   FROM pr_handoff_bindings b JOIN review_policies p ON p.id=? AND p.owner_id=b.owner_id AND p.repository_id=b.repo_id
   JOIN trigger_events e ON e.id=? AND e.user_id=b.owner_id
   JOIN triggers t ON t.id=e.trigger_id AND t.user_id=b.owner_id AND t.project_id=p.project_id
    AND t.kind='integration' AND t.workflow=p.workflow_ref AND t.config->>'integration'='github'
   JOIN workflows w ON w.id=e.admission_workflow_id AND w.parent_id IS NULL AND w.owner_user_id=b.owner_id
   JOIN chats c ON c.id=w.chat_id AND c.user_id=b.owner_id AND c.project_id=p.project_id
   WHERE b.id=? AND p.enabled AND e.admission_state='reserved' AND e.superseded_by IS NULL
   AND e.kind='integration' AND e.admission_scope=p.admission_scope
   AND e.admission_key=('github:'||b.repo_id::text||':pr:'||b.pr_number::text)
   AND w.workflow_name=p.workflow_ref AND e.admission_head=b.head_sha
   AND e.payload->>'integration'='github' AND e.payload->>'event' LIKE 'pull_request.%'
   AND e.payload->>'resource'=b.repo_id::text AND e.payload->'data'->'pull_request'->>'number'=b.pr_number::text
   AND e.admission_workflow_id<>b.root_id AND length(e.admission_run_id)>0`, in.PolicyID, in.AdmissionEventID, in.BindingID).Scan(ctx, &row)
		if errors.Is(err, sql.ErrNoRows) {
			return reviewpublisher.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if in.ID == "" || row.ReviewerRootID == "" || in.ProviderHead.RepositoryID != row.RepositoryID || in.ProviderHead.PullNumber != row.PRNumber || in.ProviderHead.SHA != row.HeadSHA {
			return reviewpublisher.ErrUnauthorized
		}
		var existing string
		err = s.DB.Bun().NewRaw(`SELECT id FROM review_assignments WHERE binding_id=? AND head_sha=?`, in.BindingID, row.HeadSHA).Scan(ctx, &existing)
		if err == nil {
			var e error
			out, e = s.LoadReviewAuthority(ctx, existing)
			return e
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var generation int64
		if err = s.DB.Bun().NewRaw(`SELECT COALESCE(MAX(generation),0)+1 FROM review_assignments WHERE binding_id=?`, in.BindingID).Scan(ctx, &generation); err != nil {
			return err
		}
		_, err = s.DB.Bun().NewRaw(`INSERT INTO review_assignments
   (id,policy_id,binding_id,admission_event_id,owner_id,repository_id,pr_number,head_sha,author_root_id,author_run_id,reviewer_root_id,reviewer_run_id,policy_revision,workflow_digest,graph_snapshot,completion_node,generation)
   VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?::jsonb,?,?)`, in.ID, in.PolicyID, in.BindingID, in.AdmissionEventID, row.OwnerID, row.RepositoryID, row.PRNumber, row.HeadSHA, row.AuthorRootID, row.AuthorRunID, row.ReviewerRootID, row.ReviewerRunID, row.Revision, row.WorkflowDigest, string(row.GraphSnapshot), row.CompletionNode, generation).Exec(ctx)
		if err != nil {
			return err
		}
		_, err = s.DB.Bun().NewRaw(`UPDATE review_assignments SET superseded=true,updated_at=now() WHERE binding_id=? AND generation<?`, in.BindingID, generation).Exec(ctx)
		if err != nil {
			return err
		}
		out, err = s.LoadReviewAuthority(ctx, in.ID)
		return err
	})
	return out, err
}

// LoadReviewAuthority joins current policy/binding/admission; stale source or
// revoked event is projected as superseded, never a successful completion.
func (s ReviewAuthorityStore) LoadReviewAuthority(ctx context.Context, id string) (ReviewAuthority, error) {
	var r struct {
		ID, PolicyID, BindingID, AdmissionEventID, OwnerID, RepoName, HeadSHA, AuthorRootID, AuthorRunID, ReviewerRootID, ReviewerRunID, WorkflowDigest, CompletionNode, CompletionState, CheckName, CurrentDigest string
		EvidenceDigest, CompletionActivityID, CompletionRunID, Verdict                                                                                                                                             sql.NullString
		RepositoryID, PRNumber, PolicyRevision, Generation, CurrentGeneration, CurrentRevision                                                                                                                     int64
		Cancelled, Superseded, GraphSucceeded, Enabled                                                                                                                                                             bool
		GraphSnapshot                                                                                                                                                                                              json.RawMessage
	}
	err := s.DB.Bun().NewRaw(`SELECT a.id,a.policy_id,a.binding_id,a.admission_event_id,a.owner_id,b.repo_name,a.repository_id,a.pr_number,a.head_sha,a.author_root_id,a.author_run_id,a.reviewer_root_id,a.reviewer_run_id,a.policy_revision,a.workflow_digest,a.graph_snapshot,a.completion_node,a.generation,a.cancelled,a.completion_state,a.verdict,a.evidence_digest,a.completion_activity_id,a.completion_run_id,a.graph_succeeded,p.enabled,p.revision AS current_revision,p.workflow_digest AS current_digest,p.check_name,
  (SELECT MAX(generation) FROM review_assignments WHERE binding_id=a.binding_id) AS current_generation,
  (a.superseded OR b.head_sha<>a.head_sha OR b.root_id<>a.author_root_id OR b.execution_id<>a.author_run_id OR
   e.admission_head IS DISTINCT FROM a.head_sha OR e.admission_workflow_id IS DISTINCT FROM a.reviewer_root_id OR
   e.admission_run_id IS DISTINCT FROM a.reviewer_run_id OR e.admission_state NOT IN ('reserved','released') OR e.superseded_by IS NOT NULL OR
   e.kind<>'integration' OR e.admission_scope IS DISTINCT FROM p.admission_scope OR e.admission_key IS DISTINCT FROM ('github:'||a.repository_id::text||':pr:'||a.pr_number::text) OR
   e.payload->>'resource' IS DISTINCT FROM a.repository_id::text OR e.payload->'data'->'pull_request'->>'number' IS DISTINCT FROM a.pr_number::text OR
   w.workflow_name IS DISTINCT FROM p.workflow_ref OR w.owner_user_id IS DISTINCT FROM a.owner_id OR c.project_id IS DISTINCT FROM p.project_id OR
   t.kind IS DISTINCT FROM 'integration' OR t.config->>'integration' IS DISTINCT FROM 'github' OR t.workflow IS DISTINCT FROM p.workflow_ref) AS superseded
  FROM review_assignments a JOIN review_policies p ON p.id=a.policy_id
  JOIN pr_handoff_bindings b ON b.id=a.binding_id JOIN trigger_events e ON e.id=a.admission_event_id
  LEFT JOIN workflows w ON w.id=e.admission_workflow_id LEFT JOIN chats c ON c.id=w.chat_id LEFT JOIN triggers t ON t.id=e.trigger_id WHERE a.id=?`, id).Scan(ctx, &r)
	if errors.Is(err, sql.ErrNoRows) {
		return ReviewAuthority{}, reviewpublisher.ErrUnauthorized
	}
	if err != nil {
		return ReviewAuthority{}, err
	}
	return ReviewAuthority{ID: r.ID, PolicyID: r.PolicyID, BindingID: r.BindingID, AdmissionEventID: r.AdmissionEventID, OwnerID: r.OwnerID, RepoName: r.RepoName,
		Binding: reviewpublisher.Binding{RepositoryID: r.RepositoryID, PullNumber: r.PRNumber, HeadSHA: r.HeadSHA, Reviewer: reviewpublisher.Execution{RootID: r.ReviewerRootID, RunID: r.ReviewerRunID}, WorkflowDigest: r.WorkflowDigest, PolicyRevision: r.PolicyRevision, Generation: r.Generation},
		Author:  reviewpublisher.Execution{RootID: r.AuthorRootID, RunID: r.AuthorRunID}, Policy: reviewpublisher.Policy{Enabled: r.Enabled, RepositoryID: r.RepositoryID, WorkflowDigest: r.CurrentDigest, Revision: r.CurrentRevision, CheckName: r.CheckName}, CurrentGeneration: r.CurrentGeneration, Cancelled: r.Cancelled, Superseded: r.Superseded,
		Completion: reviewpublisher.Completion{Final: r.CompletionState == "terminal", GraphSucceeded: r.GraphSucceeded, Verdict: reviewpublisher.Verdict(r.Verdict.String)}, GraphSnapshot: r.GraphSnapshot, CompletionNode: r.CompletionNode, EvidenceDigest: r.EvidenceDigest.String, ActivityID: r.CompletionActivityID.String, CompletionRunID: r.CompletionRunID.String}, nil
}

// RecordReviewEvidence is a trusted server receiver, not an authorization API
// for arbitrary callers. Only the exact-history verifier may call it in runtime;
// there is no registered public/agent path. SQL checks subject/policy freshness
// but cannot itself authenticate a fabricated Temporal evidence argument.
// RecordReviewEvidence persists verified pending evidence, and only later marks
// terminal when exact logical Temporal completion has been observed. Identical
// retries are accepted; conflicting verdict/activity evidence is never rewritten.
func (s ReviewAuthorityStore) RecordReviewEvidence(ctx context.Context, e ReviewEvidence) error {
	return s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		a, err := s.LoadReviewAuthority(ctx, e.AssignmentID)
		if err != nil {
			return err
		}
		if !authorityCurrent(a) || e.Digest == "" || e.ActivityID == "" || e.RunID == "" || !validReviewVerdict(e.Verdict) {
			return reviewpublisher.ErrUnauthorized
		}
		if a.EvidenceDigest != "" && (a.EvidenceDigest != e.Digest || a.ActivityID != e.ActivityID || a.CompletionRunID != e.RunID || a.Completion.Verdict != e.Verdict) {
			return reviewpublisher.ErrUnauthorized
		}
		if a.Completion.Final {
			if !e.Terminal || a.Completion.GraphSucceeded != e.GraphSucceeded {
				return reviewpublisher.ErrUnauthorized
			}
			return nil
		}
		state := "pending"
		if e.Terminal {
			state = "terminal"
		}
		_, err = s.DB.Bun().NewRaw(`UPDATE review_assignments SET completion_state=?,verdict=?,evidence_digest=?,completion_activity_id=?,completion_run_id=?,graph_succeeded=?,updated_at=now() WHERE id=?`, state, string(e.Verdict), e.Digest, e.ActivityID, e.RunID, e.Terminal && e.GraphSucceeded, e.AssignmentID).Exec(ctx)
		return err
	})
}

func authorityCurrent(a ReviewAuthority) bool {
	return a.Policy.Enabled && !a.Cancelled && !a.Superseded && a.CurrentGeneration == a.Binding.Generation && a.Policy.Revision == a.Binding.PolicyRevision && a.Policy.WorkflowDigest == a.Binding.WorkflowDigest && a.Author.RootID != a.Binding.Reviewer.RootID
}
func validReviewAdmissionSnapshot(raw []byte, workflowRef string) bool {
	var snapshot struct {
		RootRef      string                     `json:"root_ref"`
		AdmissionKey string                     `json:"admission_key"`
		Graphs       map[string]json.RawMessage `json:"graphs"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.RootRef != workflowRef || snapshot.AdmissionKey != ReviewAdmissionKeyExpression {
		return false
	}
	var root struct {
		Concurrency struct {
			Key string `json:"key"`
		} `json:"concurrency"`
	}
	return json.Unmarshal(snapshot.Graphs[workflowRef], &root) == nil && root.Concurrency.Key == ReviewAdmissionKeyExpression
}

// ReviewExternalID is fixed from immutable assignment, generation and intent.
func ReviewExternalID(a ReviewAuthority, intentID string) string {
	return "review:" + a.ID + ":" + strconv.FormatInt(a.Binding.Generation, 10) + ":" + intentID
}

func validReviewVerdict(v reviewpublisher.Verdict) bool {
	return v == reviewpublisher.Approve || v == reviewpublisher.Changes || v == reviewpublisher.Incomplete
}

// PrepareIntent implements reviewpublisher.IntentStore against live SQL authority.
func (s ReviewAuthorityStore) PrepareIntent(ctx context.Context, in reviewpublisher.Intent) (reviewpublisher.Receipt, error) {
	var receipt reviewpublisher.Receipt
	err := s.DB.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		a, err := s.LoadReviewAuthority(ctx, in.AssignmentID)
		if err != nil {
			return err
		}
		if !authorityCurrent(a) || a.Binding != in.Binding || a.Policy.CheckName != in.CheckName || !a.Completion.Final || !validReviewVerdict(a.Completion.Verdict) {
			return reviewpublisher.ErrUnauthorized
		}
		conclusion := "failure"
		if a.Completion.GraphSucceeded && a.Completion.Verdict == reviewpublisher.Approve {
			conclusion = "success"
		}
		if in.Conclusion != conclusion {
			return reviewpublisher.ErrUnauthorized
		}
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		digest, err := DigestReviewSnapshot(raw)
		if err != nil {
			return err
		}
		var old struct {
			ID      string
			Payload json.RawMessage
		}
		err = s.DB.Bun().NewRaw(`SELECT id,payload FROM review_publication_intents WHERE assignment_id=?`, in.AssignmentID).Scan(ctx, &old)
		if err == nil {
			var existing reviewpublisher.Intent
			if json.Unmarshal(old.Payload, &existing) != nil || !reflect.DeepEqual(existing, in) {
				return reviewpublisher.ErrUnauthorized
			}
			receipt = reviewpublisher.Receipt{IntentID: old.ID, Coalesced: true}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id := uuid.NewString()
		_, err = s.DB.Bun().NewRaw(`INSERT INTO review_publication_intents(id,assignment_id,repository_id,payload,payload_digest) VALUES(?,?,?,?::jsonb,?)`, id, in.AssignmentID, in.Binding.RepositoryID, string(raw), digest).Exec(ctx)
		if err == nil {
			receipt = reviewpublisher.Receipt{IntentID: id}
		}
		return err
	})
	return receipt, err
}
