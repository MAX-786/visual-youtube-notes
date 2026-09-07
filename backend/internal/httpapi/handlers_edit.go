package httpapi

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/edit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

// editError maps the write side's typed failures onto the error envelope,
// with the sentence spec 15 asks for: what happened and what to do next.
func editError(err error) error {
	var refusal *edit.Refusal
	var invalid *edit.ValidationError
	switch {
	case errors.Is(err, edit.ErrNotFound):
		return apierr.New(apierr.NotFound, "No such note or version.")
	case errors.As(err, &invalid):
		return apierr.New(apierr.ValidationFailed, "That edit would break the note: "+invalid.Rule.Message+".").With("rule", invalid.Rule.Rule).With("element_id", invalid.Rule.ElementID)
	case errors.As(err, &refusal):
		messages := map[string]string{
			"note_trashed":                   "This note is in Trash. Restore it before editing.",
			"note_not_ready":                 "This note is still being made. Edit it once it is ready.",
			"revision_not_active":            "That version is waiting for a decision. Activate or discard it first.",
			"note_purged":                    "This note has been purged and cannot be edited.",
			"mutation_expired":               "This edit is more than thirty days old and was not applied. Make it again.",
			"revision_not_awaiting_decision": "That version is not waiting for a decision.",
			"takeaway_stale":                 "Your takeaway changed on another device. Merge the two texts and save again.",
			"topic_exists":                   "A topic with that name already exists.",
			"tag_exists":                     "A tag with that name already exists.",
			"topic_in_use":                   "Notes still file under this topic. Merge it into another topic first.",
			"invalid_request":                "The request is not valid.",
		}
		message, ok := messages[refusal.Code]
		if !ok {
			return apierr.Internal(err)
		}
		out := apierr.New(apierr.Code(refusal.Code), message)
		for k, v := range refusal.Details {
			out = out.With(k, v)
		}
		return out
	default:
		return apierr.Internal(err)
	}
}

// toOps converts the generated discriminated union into the flat catalogue.
func toOps(ops []MutationOp) ([]generation.Op, error) {
	out := make([]generation.Op, 0, len(ops))
	for _, raw := range ops {
		value, err := raw.ValueByDiscriminator()
		if err != nil {
			return nil, apierr.New(apierr.InvalidRequest, "An operation is not in the catalogue.").With("field", "ops")
		}
		var op generation.Op
		switch v := value.(type) {
		case SetBlockTextOp:
			op = generation.Op{Kind: generation.OpSetBlockText, Section: string(v.Section), Text: v.Text}
			if v.Id != nil {
				op.ID = string(*v.Id)
			}
		case InsertBlockOp:
			op = generation.Op{Kind: generation.OpInsertBlock, Section: string(v.Section), ID: string(v.Id), Text: v.Text}
			if v.AfterId != nil {
				after := string(*v.AfterId)
				op.AfterID = &after
			}
		case DeleteBlockOp:
			op = generation.Op{Kind: generation.OpDeleteBlock, Section: string(v.Section), ID: string(v.Id)}
		case ReorderBlocksOp:
			op = generation.Op{Kind: generation.OpReorderBlocks, Section: string(v.Section), IDs: toStrings(v.Ids)}
		case SetElementLabelOp:
			op = generation.Op{Kind: generation.OpSetElementLabel, ElementID: string(v.ElementId), Label: v.Label}
		case SetCellTextOp:
			op = generation.Op{Kind: generation.OpSetCellText, RowID: string(v.RowId), ColumnID: string(v.ColumnId), Text: v.Text}
		case InsertElementOp:
			op = generation.Op{Kind: generation.OpInsertElement, Collection: string(v.Collection), ID: string(v.Id)}
			if v.Label != nil {
				op.Label = *v.Label
			}
			if v.AfterId != nil {
				after := string(*v.AfterId)
				op.AfterID = &after
			}
			if v.ParentId != nil {
				op.ParentID = string(*v.ParentId)
			}
			if v.From != nil {
				op.From = string(*v.From)
			}
			if v.To != nil {
				op.To = string(*v.To)
			}
		case DeleteElementOp:
			op = generation.Op{Kind: generation.OpDeleteElement, ElementID: string(v.ElementId)}
		case ReorderElementsOp:
			op = generation.Op{Kind: generation.OpReorderElements, Collection: string(v.Collection), IDs: toStrings(v.Ids)}
		default:
			return nil, apierr.New(apierr.InvalidRequest, "An operation is not in the catalogue.").With("field", "ops")
		}
		out = append(out, op)
	}
	return out, nil
}

func toStrings(ids []ElementId) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// MutateNote is POST /notes/{noteId}/mutations.
func (s *Server) MutateNote(ctx context.Context, req MutateNoteRequestObject) (MutateNoteResponseObject, error) {
	session := SessionFrom(ctx)
	ops, err := toOps(req.Body.Ops)
	if err != nil {
		return nil, err
	}
	result, err := s.deps.Edit.Mutate(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), edit.Mutation{
		ID: uuid.UUID(req.Body.MutationId), BaseRevisionID: uuid.UUID(req.Body.BaseRevisionId), Ops: ops,
	})
	if err != nil {
		return nil, editError(err)
	}
	return MutateNote200JSONResponse(MutationResult{
		MutationId: Uuid(req.Body.MutationId), Outcome: MutationOutcome(result.Outcome),
		RevisionId: Uuid(result.RevisionID), ActiveRevisionId: Uuid(result.ActiveRevisionID),
		SyncCursor: result.SyncCursor, Replayed: result.Replayed,
	}), nil
}

func toRevisionSummaryFromEdit(r *edit.Revision) RevisionSummary {
	out := RevisionSummary{
		Id: Uuid(r.ID), RevisionNumber: r.Number, Origin: RevisionOrigin(r.Origin),
		GenerationInstruction: r.GenerationInstruction, AwaitingDecisionSince: r.AwaitingDecisionSince,
		IsActive: r.IsActive, CreatedAt: r.CreatedAt,
	}
	if r.ParentID != nil {
		id := Uuid(*r.ParentID)
		out.ParentRevisionId = &id
	}
	if r.OriginDeviceSessionID != nil {
		id := Uuid(*r.OriginDeviceSessionID)
		out.OriginDeviceSessionId = &id
	}
	return out
}

// ListRevisions is GET /notes/{noteId}/revisions.
func (s *Server) ListRevisions(ctx context.Context, req ListRevisionsRequestObject) (ListRevisionsResponseObject, error) {
	session := SessionFrom(ctx)
	revisions, err := s.deps.Edit.ListRevisions(ctx, session.UserID, uuid.UUID(req.NoteId))
	if err != nil {
		return nil, editError(err)
	}
	out := RevisionList{Revisions: make([]RevisionSummary, 0, len(revisions))}
	for i := range revisions {
		out.Revisions = append(out.Revisions, toRevisionSummaryFromEdit(&revisions[i]))
	}
	return ListRevisions200JSONResponse(out), nil
}

// GetRevision is GET /notes/{noteId}/revisions/{revisionId}.
func (s *Server) GetRevision(ctx context.Context, req GetRevisionRequestObject) (GetRevisionResponseObject, error) {
	session := SessionFrom(ctx)
	r, err := s.deps.Edit.GetRevision(ctx, session.UserID, uuid.UUID(req.NoteId), uuid.UUID(req.RevisionId))
	if err != nil {
		return nil, editError(err)
	}
	summary := toRevisionSummaryFromEdit(r)
	rev := Revision{
		Id: summary.Id, RevisionNumber: summary.RevisionNumber, Origin: summary.Origin, ParentRevisionId: summary.ParentRevisionId,
		GenerationInstruction: summary.GenerationInstruction, AwaitingDecisionSince: summary.AwaitingDecisionSince,
		IsActive: summary.IsActive, OriginDeviceSessionId: summary.OriginDeviceSessionId, CreatedAt: summary.CreatedAt,
	}
	if err := json.Unmarshal(r.Payload, &rev.Payload); err != nil {
		return nil, apierr.Internal(err)
	}
	return GetRevision200JSONResponse(rev), nil
}

func pointer(p *edit.PointerResult) RevisionPointerResult {
	return RevisionPointerResult{NoteId: Uuid(p.NoteID), RevisionId: Uuid(p.RevisionID), ActiveRevisionId: Uuid(p.ActiveRevisionID), SyncCursor: p.SyncCursor}
}

// ActivateRevision is POST /notes/{noteId}/revisions/{revisionId}/activate.
func (s *Server) ActivateRevision(ctx context.Context, req ActivateRevisionRequestObject) (ActivateRevisionResponseObject, error) {
	session := SessionFrom(ctx)
	p, err := s.deps.Edit.Activate(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), uuid.UUID(req.RevisionId))
	if err != nil {
		return nil, editError(err)
	}
	return ActivateRevision200JSONResponse(pointer(p)), nil
}

// RestoreRevision is POST /notes/{noteId}/revisions/{revisionId}/restore.
func (s *Server) RestoreRevision(ctx context.Context, req RestoreRevisionRequestObject) (RestoreRevisionResponseObject, error) {
	session := SessionFrom(ctx)
	p, err := s.deps.Edit.Restore(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), uuid.UUID(req.RevisionId))
	if err != nil {
		return nil, editError(err)
	}
	return RestoreRevision200JSONResponse(pointer(p)), nil
}

// DiscardRevision is POST /notes/{noteId}/revisions/{revisionId}/discard.
func (s *Server) DiscardRevision(ctx context.Context, req DiscardRevisionRequestObject) (DiscardRevisionResponseObject, error) {
	session := SessionFrom(ctx)
	p, err := s.deps.Edit.Discard(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), uuid.UUID(req.RevisionId))
	if err != nil {
		return nil, editError(err)
	}
	return DiscardRevision200JSONResponse(pointer(p)), nil
}

// SetTakeaway is PUT /notes/{noteId}/takeaway.
func (s *Server) SetTakeaway(ctx context.Context, req SetTakeawayRequestObject) (SetTakeawayResponseObject, error) {
	session := SessionFrom(ctx)
	t, err := s.deps.Edit.SetTakeaway(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), req.Body.Text, req.Body.BaseCursor)
	if err != nil {
		return nil, editError(err)
	}
	return SetTakeaway200JSONResponse(TakeawayResult{Takeaway: Takeaway{Text: t.Text, UpdatedCursor: t.UpdatedCursor, UpdatedAt: t.UpdatedAt}, SyncCursor: t.SyncCursor}), nil
}

// SetPrimaryTopic is PUT /notes/{noteId}/topic.
func (s *Server) SetPrimaryTopic(ctx context.Context, req SetPrimaryTopicRequestObject) (SetPrimaryTopicResponseObject, error) {
	session := SessionFrom(ctx)
	var topicID *uuid.UUID
	if req.Body.TopicId != nil {
		id := uuid.UUID(*req.Body.TopicId)
		topicID = &id
	}
	name := ""
	if req.Body.Name != nil {
		name = *req.Body.Name
	}
	ref, cursor, err := s.deps.Edit.SetPrimaryTopic(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), topicID, name)
	if err != nil {
		return nil, editError(err)
	}
	return SetPrimaryTopic200JSONResponse(PrimaryTopicResult{PrimaryTopic: TopicRef{Id: Uuid(ref.ID), Name: ref.Name}, SyncCursor: cursor}), nil
}

// SetTags is PUT /notes/{noteId}/tags.
func (s *Server) SetTags(ctx context.Context, req SetTagsRequestObject) (SetTagsResponseObject, error) {
	session := SessionFrom(ctx)
	refs, cursor, err := s.deps.Edit.SetTags(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), req.Body.Tags)
	if err != nil {
		return nil, editError(err)
	}
	out := NoteTagsResult{Tags: make([]TagRef, 0, len(refs)), SyncCursor: cursor}
	for _, r := range refs {
		out.Tags = append(out.Tags, TagRef{Id: Uuid(r.ID), Name: r.Name})
	}
	return SetTags200JSONResponse(out), nil
}

// UpdateLayout is PATCH /notes/{noteId}/layout.
func (s *Server) UpdateLayout(ctx context.Context, req UpdateLayoutRequestObject) (UpdateLayoutResponseObject, error) {
	session := SessionFrom(ctx)
	positions := make(map[string]generation.Position, len(req.Body.Positions))
	for id, p := range req.Body.Positions {
		positions[id] = generation.Position{X: p.X, Y: p.Y, W: p.W, H: p.H}
	}
	layout, err := s.deps.Edit.UpdateLayout(ctx, session.UserID, session.ID, uuid.UUID(req.NoteId), positions)
	if err != nil {
		return nil, editError(err)
	}
	merged := make(PositionMap, len(layout.Positions))
	for id, p := range layout.Positions {
		merged[id] = Position{X: p.X, Y: p.Y, W: p.W, H: p.H}
	}
	updated := layout.UpdatedAt
	return UpdateLayout200JSONResponse(LayoutResult{Layout: DiagramLayout{Positions: merged, UpdatedAt: &updated}, SyncCursor: layout.SyncCursor}), nil
}

func toTopic(n *edit.Named) Topic {
	return Topic{Id: Uuid(n.ID), Name: n.Name, NormalizedName: n.Normalized, NoteCount: n.NoteCount, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}
}

func toTag(n *edit.Named) Tag {
	return Tag{Id: Uuid(n.ID), Name: n.Name, NormalizedName: n.Normalized, NoteCount: n.NoteCount, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}
}

// ListTopics is GET /topics.
func (s *Server) ListTopics(ctx context.Context, _ ListTopicsRequestObject) (ListTopicsResponseObject, error) {
	session := SessionFrom(ctx)
	topics, err := s.deps.Edit.ListTopics(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := TopicList{Topics: make([]Topic, 0, len(topics))}
	for i := range topics {
		out.Topics = append(out.Topics, toTopic(&topics[i]))
	}
	return ListTopics200JSONResponse(out), nil
}

// CreateTopic is POST /topics.
func (s *Server) CreateTopic(ctx context.Context, req CreateTopicRequestObject) (CreateTopicResponseObject, error) {
	session := SessionFrom(ctx)
	t, err := s.deps.Edit.CreateTopic(ctx, session.UserID, session.ID, req.Body.Name)
	if err != nil {
		return nil, editError(err)
	}
	return CreateTopic201JSONResponse(toTopic(t)), nil
}

// UpdateTopic is PATCH /topics/{topicId}.
func (s *Server) UpdateTopic(ctx context.Context, req UpdateTopicRequestObject) (UpdateTopicResponseObject, error) {
	session := SessionFrom(ctx)
	t, err := s.deps.Edit.RenameTopic(ctx, session.UserID, session.ID, uuid.UUID(req.TopicId), req.Body.Name)
	if err != nil {
		return nil, editError(err)
	}
	return UpdateTopic200JSONResponse(toTopic(t)), nil
}

// DeleteTopic is DELETE /topics/{topicId}.
func (s *Server) DeleteTopic(ctx context.Context, req DeleteTopicRequestObject) (DeleteTopicResponseObject, error) {
	session := SessionFrom(ctx)
	if err := s.deps.Edit.DeleteTopic(ctx, session.UserID, session.ID, uuid.UUID(req.TopicId)); err != nil {
		return nil, editError(err)
	}
	return DeleteTopic204Response{}, nil
}

// MergeTopic is POST /topics/{topicId}/merge.
func (s *Server) MergeTopic(ctx context.Context, req MergeTopicRequestObject) (MergeTopicResponseObject, error) {
	session := SessionFrom(ctx)
	t, err := s.deps.Edit.MergeTopic(ctx, session.UserID, session.ID, uuid.UUID(req.TopicId), uuid.UUID(req.Body.IntoTopicId))
	if err != nil {
		return nil, editError(err)
	}
	return MergeTopic200JSONResponse(toTopic(t)), nil
}

// ListTags is GET /tags.
func (s *Server) ListTags(ctx context.Context, _ ListTagsRequestObject) (ListTagsResponseObject, error) {
	session := SessionFrom(ctx)
	tags, err := s.deps.Edit.ListTags(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := TagList{Tags: make([]Tag, 0, len(tags))}
	for i := range tags {
		out.Tags = append(out.Tags, toTag(&tags[i]))
	}
	return ListTags200JSONResponse(out), nil
}

// UpdateTag is PATCH /tags/{tagId}.
func (s *Server) UpdateTag(ctx context.Context, req UpdateTagRequestObject) (UpdateTagResponseObject, error) {
	session := SessionFrom(ctx)
	t, err := s.deps.Edit.RenameTag(ctx, session.UserID, session.ID, uuid.UUID(req.TagId), req.Body.Name)
	if err != nil {
		return nil, editError(err)
	}
	return UpdateTag200JSONResponse(toTag(t)), nil
}

// DeleteTag is DELETE /tags/{tagId}.
func (s *Server) DeleteTag(ctx context.Context, req DeleteTagRequestObject) (DeleteTagResponseObject, error) {
	session := SessionFrom(ctx)
	if err := s.deps.Edit.DeleteTag(ctx, session.UserID, session.ID, uuid.UUID(req.TagId)); err != nil {
		return nil, editError(err)
	}
	return DeleteTag204Response{}, nil
}
