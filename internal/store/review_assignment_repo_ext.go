package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"

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
type ReviewPolicyConfig struct {
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
	if err != nil || digest != p.WorkflowDigest || p.ID == "" || p.OwnerID == "" || p.AuthorizedBy == "" || p.RepositoryID <= 0 || p.Revision <= 0 || p.CheckName != "ReliantReview" || p.CompletionNode == "" {
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
		_, err = s.DB.Bun().NewRaw(`INSERT INTO review_policies (id,owner_id,repository_id,revision,enabled,workflow_digest,graph_snapshot,completion_node,check_name,authorized_by)
   VALUES (?,?,?,?,?,?,?::jsonb,?,?,?) ON CONFLICT (id) DO UPDATE SET revision=EXCLUDED.revision,enabled=EXCLUDED.enabled,
   workflow_digest=EXCLUDED.workflow_digest,graph_snapshot=EXCLUDED.graph_snapshot,completion_node=EXCLUDED.completion_node,check_name=EXCLUDED.check_name,authorized_by=EXCLUDED.authorized_by,updated_at=now()
   WHERE review_policies.owner_id=EXCLUDED.owner_id AND review_policies.repository_id=EXCLUDED.repository_id`, p.ID, p.OwnerID, p.RepositoryID, p.Revision, p.Enabled, p.WorkflowDigest, string(p.GraphSnapshot), p.CompletionNode, p.CheckName, p.AuthorizedBy).Exec(ctx)
		return err
	})
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
   WHERE b.id=? AND p.enabled AND e.admission_state='reserved' AND e.superseded_by IS NULL
   AND e.admission_head=b.head_sha AND e.admission_workflow_id<>b.root_id AND length(e.admission_run_id)>0`, in.PolicyID, in.AdmissionEventID, in.BindingID).Scan(ctx, &row)
		if errors.Is(err, sql.ErrNoRows) {
			return reviewpublisher.ErrUnauthorized
		}
		if err != nil {
			return err
		}
		if in.ID == "" || row.ReviewerRootID == "" {
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
   e.admission_run_id IS DISTINCT FROM a.reviewer_run_id OR e.admission_state NOT IN ('reserved','released') OR e.superseded_by IS NOT NULL) AS superseded
  FROM review_assignments a JOIN review_policies p ON p.id=a.policy_id
  JOIN pr_handoff_bindings b ON b.id=a.binding_id JOIN trigger_events e ON e.id=a.admission_event_id WHERE a.id=?`, id).Scan(ctx, &r)
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
		_, err = s.DB.Bun().NewRaw(`INSERT INTO review_publication_intents(id,assignment_id,payload,payload_digest) VALUES(?,?,?::jsonb,?)`, id, in.AssignmentID, string(raw), digest).Exec(ctx)
		if err == nil {
			receipt = reviewpublisher.Receipt{IntentID: id}
		}
		return err
	})
	return receipt, err
}
