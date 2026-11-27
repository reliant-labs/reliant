// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/reliant-labs/reliant/internal/gtm/library"
	"github.com/reliant-labs/reliant/internal/store"
)

var (
	ideaKinds    = []IdeaKind{IdeaKindProduct, IdeaKindGTM, IdeaKindContent}
	ideaStatuses = []IdeaStatus{IdeaProposed, IdeaAccepted, IdeaDone, IdeaRejected}
)

// ListIdeas lists the org's ideas, newest first.
func (s *service) ListIdeas(ctx context.Context, actor Actor, in ListIdeasInput) (ListIdeasResult, error) {
	if err := s.admit(ctx, actor); err != nil {
		return ListIdeasResult{}, err
	}
	limit, offset, err := page(in.PageSize, in.PageToken, DefaultIdeaPageSize, MaxIdeaPageSize)
	if err != nil {
		return ListIdeasResult{}, err
	}
	if in.Kind != "" && !slices.Contains(ideaKinds, in.Kind) {
		return ListIdeasResult{}, invalid("kind", "unknown kind")
	}
	if in.Status != "" && !slices.Contains(ideaStatuses, in.Status) {
		return ListIdeasResult{}, invalid("status", "unknown status")
	}
	rows, err := s.store.ListIdeas(ctx, IdeaFilter{
		OrgID: actor.OrgID, Kind: in.Kind, Status: in.Status, CampaignID: in.CampaignID,
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return ListIdeasResult{}, err
	}
	rows, next := nextToken(rows, limit, offset)
	ideas, err := ideasFromRows(rows)
	if err != nil {
		return ListIdeasResult{}, err
	}
	return ListIdeasResult{Ideas: ideas, NextPageToken: next}, nil
}

// GetIdea returns one of the org's ideas.
func (s *service) GetIdea(ctx context.Context, actor Actor, in GetIdeaInput) (Idea, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Idea{}, err
	}
	if err := checkID("id", in.ID); err != nil {
		return Idea{}, err
	}
	row, err := s.store.GetIdea(ctx, actor.OrgID, in.ID)
	if err != nil {
		return Idea{}, s.notFound(ctx, err, "idea", in.ID)
	}
	return ideaFromRow(row)
}

// CreateIdea creates a PROPOSED idea; an agent's is sourced AGENT with its
// chat.
func (s *service) CreateIdea(ctx context.Context, actor Actor, in CreateIdeaInput) (Idea, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Idea{}, err
	}
	if !slices.Contains(ideaKinds, in.Kind) {
		return Idea{}, invalid("kind", "required")
	}
	if err := firstErr(
		checkLen("title", in.Title, 1, MaxTitleLen),
		checkLen("body_md", in.BodyMD, 0, MaxBodyLen),
		checkEvidence(in.Evidence),
		checkIdeaLibrary(in.LibraryKey, in.TailorAnswers, in.SuggestedWorkflowMD),
	); err != nil {
		return Idea{}, err
	}
	ev, err := evidenceJSON(in.Evidence)
	if err != nil {
		return Idea{}, err
	}
	answers, err := tailorAnswersJSON(in.TailorAnswers)
	if err != nil {
		return Idea{}, err
	}
	source, chat := IdeaSourceHuman, ""
	if !actor.Person() {
		source, chat = IdeaSourceAgent, actor.ChatID
	}
	now := s.now()
	var out Idea
	err = s.store.InTx(ctx, func(ctx context.Context) error {
		if err := s.checkCampaignLink(ctx, actor.OrgID, in.CampaignID); err != nil {
			return err
		}
		row := &store.GtmIdea{
			OrgID:               actor.OrgID,
			Kind:                string(in.Kind),
			Title:               in.Title,
			BodyMd:              in.BodyMD,
			Evidence:            ev,
			Status:              string(IdeaProposed),
			Source:              string(source),
			SourceChatID:        chat,
			CreatedBy:           actor.UserID,
			CampaignID:          strPtrOrNil(in.CampaignID),
			LibraryKey:          strPtrOrNil(in.LibraryKey),
			TailorAnswers:       answers,
			SuggestedWorkflowMd: in.SuggestedWorkflowMD,
			Revision:            1,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
		if err := s.store.CreateIdea(ctx, row); err != nil {
			return err
		}
		var err error
		out, err = ideaFromRow(row)
		return err
	})
	return out, err
}

// UpdateIdea changes an idea's fields; an agent edits PROPOSED ideas only.
func (s *service) UpdateIdea(ctx context.Context, actor Actor, in UpdateIdeaInput) (Idea, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Idea{}, err
	}
	if err := firstErr(checkID("id", in.ID), checkRevision(in.ExpectedRevision)); err != nil {
		return Idea{}, err
	}
	if in.Kind != nil && !slices.Contains(ideaKinds, *in.Kind) {
		return Idea{}, invalid("kind", "unknown kind")
	}
	if in.Title != nil {
		if err := checkLen("title", *in.Title, 1, MaxTitleLen); err != nil {
			return Idea{}, err
		}
	}
	if in.BodyMD != nil {
		if err := checkLen("body_md", *in.BodyMD, 0, MaxBodyLen); err != nil {
			return Idea{}, err
		}
	}
	if in.Evidence != nil {
		if err := checkEvidence(*in.Evidence); err != nil {
			return Idea{}, err
		}
	}
	var key, workflow string
	var answers []TailorAnswer
	if in.LibraryKey != nil {
		key = *in.LibraryKey
	}
	if in.TailorAnswers != nil {
		answers = *in.TailorAnswers
	}
	if in.SuggestedWorkflowMD != nil {
		workflow = *in.SuggestedWorkflowMD
	}
	if err := checkIdeaLibrary(key, answers, workflow); err != nil {
		return Idea{}, err
	}
	var out Idea
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetIdea(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "idea", in.ID)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if !actor.Person() && IdeaStatus(row.Status) != IdeaProposed {
			return personOnly("edit an idea that is no longer proposed")
		}
		if in.Kind != nil {
			row.Kind = string(*in.Kind)
		}
		if in.Title != nil {
			row.Title = *in.Title
		}
		if in.BodyMD != nil {
			row.BodyMd = *in.BodyMD
		}
		if in.Evidence != nil {
			if row.Evidence, err = evidenceJSON(*in.Evidence); err != nil {
				return err
			}
		}
		if in.CampaignID != nil {
			if err := s.checkCampaignLink(ctx, actor.OrgID, *in.CampaignID); err != nil {
				return err
			}
			row.CampaignID = strPtrOrNil(*in.CampaignID)
		}
		if in.LibraryKey != nil {
			row.LibraryKey = strPtrOrNil(*in.LibraryKey)
		}
		if in.TailorAnswers != nil {
			if row.TailorAnswers, err = tailorAnswersJSON(*in.TailorAnswers); err != nil {
				return err
			}
		}
		if in.SuggestedWorkflowMD != nil {
			row.SuggestedWorkflowMd = *in.SuggestedWorkflowMD
		}
		out, err = s.writeIdea(ctx, actor.OrgID, row, in.ExpectedRevision)
		return err
	})
	return out, err
}

// SetIdeaStatus moves an idea to a status: a person's call.
func (s *service) SetIdeaStatus(ctx context.Context, actor Actor, in SetIdeaStatusInput) (Idea, error) {
	if err := s.admit(ctx, actor); err != nil {
		return Idea{}, err
	}
	if err := firstErr(checkID("id", in.ID), checkRevision(in.ExpectedRevision)); err != nil {
		return Idea{}, err
	}
	if !slices.Contains(ideaStatuses, in.Status) {
		return Idea{}, invalid("status", "required")
	}
	if !actor.Person() && (in.Status == IdeaAccepted || in.Status == IdeaRejected) {
		return Idea{}, personOnly("accept or reject an idea")
	}
	var out Idea
	err := s.store.InTx(ctx, func(ctx context.Context) error {
		row, err := s.store.GetIdea(ctx, actor.OrgID, in.ID)
		if err != nil {
			return s.notFound(ctx, err, "idea", in.ID)
		}
		if row.Revision != in.ExpectedRevision {
			return conflict(in.ID, in.ExpectedRevision, row.Revision)
		}
		if IdeaStatus(row.Status) == in.Status {
			out, err = ideaFromRow(row)
			return err
		}
		// An agent edits proposed ideas and proposes; every move out of (or
		// back into) PROPOSED is a person's.
		if !actor.Person() {
			return personOnly("change an idea's status")
		}
		row.Status = string(in.Status)
		out, err = s.writeIdea(ctx, actor.OrgID, row, in.ExpectedRevision)
		return err
	})
	return out, err
}

// writeIdea writes row at expected and reads back what was stored.
func (s *service) writeIdea(ctx context.Context, orgID string, row *store.GtmIdea, expected int32) (Idea, error) {
	row.UpdatedAt = s.now()
	if err := s.store.UpdateIdea(ctx, row, expected); err != nil {
		return Idea{}, s.writeErr(err, row.ID, expected, func() (int32, error) {
			cur, err := s.store.GetIdea(ctx, orgID, row.ID)
			if err != nil {
				return 0, err
			}
			return cur.Revision, nil
		})
	}
	stored, err := s.store.GetIdea(ctx, orgID, row.ID)
	if err != nil {
		return Idea{}, s.notFound(ctx, err, "idea", row.ID)
	}
	return ideaFromRow(stored)
}

// checkCampaignLink is rule 2 for a link: id is "" or a campaign of the org.
func (s *service) checkCampaignLink(ctx context.Context, orgID, id string) error {
	if id == "" {
		return nil
	}
	if _, err := s.store.GetCampaign(ctx, orgID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return &LinkError{Field: "campaign_id", ID: id}
		}
		return err
	}
	return nil
}

// checkIdeaLink is checkCampaignLink for an idea.
func (s *service) checkIdeaLink(ctx context.Context, orgID, id string) error {
	if id == "" {
		return nil
	}
	if _, err := s.store.GetIdea(ctx, orgID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return &LinkError{Field: "idea_id", ID: id}
		}
		return err
	}
	return nil
}

// writeErr turns a store's bare ErrRevisionConflict (the row moved between
// the read and the write) into a *RevisionConflictError with the revision
// it is at now.
func (s *service) writeErr(err error, id string, expected int32, current func() (int32, error)) error {
	var typed *RevisionConflictError
	if !errors.Is(err, ErrRevisionConflict) || errors.As(err, &typed) {
		return err
	}
	rev, curErr := current()
	if curErr != nil {
		s.log.Warn("gtm: reading the revision of a conflicted row failed", "id", id, "error", curErr)
		return conflict(id, expected, 0)
	}
	return conflict(id, expected, rev)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// checkIdeaLibrary is the idea bank rule for an idea's fields: a library
// key the bank holds (or none), at most MaxTailorAnswers answers, each with
// a question, and a suggested workflow within MaxSuggestedWorkflowLen.
func checkIdeaLibrary(key string, answers []TailorAnswer, workflow string) error {
	if key != "" {
		if _, ok := library.Get(key); !ok {
			return invalid("library_key", fmt.Sprintf("%q is not an entry of the idea bank", key))
		}
	}
	if len(answers) > MaxTailorAnswers {
		return invalid("tailor_answers", fmt.Sprintf("at most %d", MaxTailorAnswers))
	}
	for i, a := range answers {
		if err := firstErr(
			checkLen(fmt.Sprintf("tailor_answers[%d].question", i), a.Question, 1, MaxTailorAnswerLen),
			checkLen(fmt.Sprintf("tailor_answers[%d].answer", i), a.Answer, 0, MaxTailorAnswerLen),
		); err != nil {
			return err
		}
	}
	return checkLen("suggested_workflow_md", workflow, 0, MaxSuggestedWorkflowLen)
}
