package httpapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/cost"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/search"
)

// queryEmbeddingTimeout bounds the one network call inside a search.
const queryEmbeddingTimeout = 8 * time.Second

// Search is GET /search: Hybrid search with three ways to degrade to
// lexical results, each named so the client can word it (ADR 0005,
// ADR 0007).
func (s *Server) Search(ctx context.Context, req SearchRequestObject) (SearchResponseObject, error) {
	session := SessionFrom(ctx)
	q := strings.TrimSpace(req.Params.Q)
	if q == "" {
		return nil, apierr.New(apierr.InvalidRequest, "Type something to search for.")
	}
	filters := search.Filters{ChannelID: req.Params.ChannelId, MinDuration: req.Params.MinDuration, MaxDuration: req.Params.MaxDuration}
	if req.Params.From != nil {
		from := req.Params.From.Format("2006-01-02")
		filters.From = &from
	}
	if req.Params.To != nil {
		to := req.Params.To.Format("2006-01-02")
		filters.To = &to
	}
	if req.Params.TopicId != nil {
		id := uuid.UUID(*req.Params.TopicId)
		filters.TopicID = &id
	}
	caseSensitive := req.Params.CaseSensitive != nil && *req.Params.CaseSensitive
	vector := ""
	var unavailable *SemanticUnavailableReason
	if !caseSensitive {
		vector, unavailable = s.queryEmbedding(ctx, session, q)
	}
	request := search.Request{UserID: session.UserID, Query: q, Vector: vector, CaseSensitive: caseSensitive, Filters: filters}
	if req.Params.Cursor != nil {
		request.Cursor = string(*req.Params.Cursor)
	}
	page, err := search.Query(ctx, s.deps.Pool, request)
	if errors.Is(err, search.ErrBadCursor) {
		return nil, apierr.New(apierr.InvalidRequest, "That cursor is not one this server issued.")
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	ids := make([]uuid.UUID, 0, len(page.Hits))
	for _, h := range page.Hits {
		ids = append(ids, h.NoteID)
	}
	cards, err := s.deps.Notes.Cards(ctx, session.UserID, ids)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := SearchResponse{Results: []SearchResult{}, Exhausted: page.Exhausted, SemanticUnavailable: unavailable}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	for _, h := range page.Hits {
		card, ok := cards[h.NoteID]
		if !ok {
			continue
		}
		r := SearchResult{Note: toNoteCard(card), Score: h.Score, MatchedKinds: []SearchChildKind{}, Excerpts: []Excerpt{}}
		for _, k := range h.MatchedKinds {
			r.MatchedKinds = append(r.MatchedKinds, SearchChildKind(k))
		}
		for _, e := range h.Excerpts {
			r.Excerpts = append(r.Excerpts, Excerpt{ChunkId: e.ChunkID, Text: e.Text})
		}
		out.Results = append(out.Results, r)
	}
	return Search200JSONResponse(out), nil
}

// queryEmbedding embeds the query under the active model, or says why not:
// the query_embedding class (never a 429 here), the Soft cap, or a fault.
// Each call is a ledger row with no Note.
func (s *Server) queryEmbedding(ctx context.Context, session *Session, q string) (string, *SemanticUnavailableReason) {
	reason := func(r SemanticUnavailableReason) (string, *SemanticUnavailableReason) { return "", &r }
	if s.deps.AI == nil {
		return reason(SemanticUnavailableReasonProviderError)
	}
	if s.deps.Limiter != nil {
		if class, ok := s.deps.Limiter.Class("query_embedding"); ok {
			ip := clientIP(requestFrom(ctx))
			if _, admitted := s.deps.Limiter.Check(ctx, class, "session", session.ID.String(), ip, session.ID.String()); !admitted {
				return reason(SemanticUnavailableReasonRateLimited)
			}
		}
	}
	allowed, err := cost.SemanticAllowed(ctx, s.deps.Pool, session.UserID)
	if err != nil {
		s.deps.Logger.Warn("cap check failed", "err", err)
		return reason(SemanticUnavailableReasonProviderError)
	}
	if !allowed {
		return reason(SemanticUnavailableReasonSoftCap)
	}
	creds, err := s.deps.AI.Credentials(ctx, s.deps.Pool, session.UserID, ai.RoleEmbeddings)
	if err != nil {
		return reason(SemanticUnavailableReasonProviderError)
	}
	var active string
	if err := s.deps.Pool.QueryRow(ctx, `SELECT active_embedding_model FROM search_index_state`).Scan(&active); err != nil || active != creds.Model {
		return reason(SemanticUnavailableReasonProviderError)
	}
	usageID, err := ai.OpenUsage(ctx, s.deps.Pool, ai.Call{UserID: session.UserID, Role: creds.Role, Provider: creds.Provider, Model: creds.Model, Estimated: ai.UnitMap{"embedding_tokens": 20}})
	if err != nil {
		return reason(SemanticUnavailableReasonProviderError)
	}
	callCtx, cancel := context.WithTimeout(ctx, queryEmbeddingTimeout)
	defer cancel()
	res, err := ai.NewClient(*creds, s.deps.AITransport).Embed(callCtx, []string{q})
	if err != nil || len(res.Vectors) != 1 {
		_ = ai.CloseUsage(context.WithoutCancel(ctx), s.deps.Pool, usageID, ai.Outcome{ErrorCode: "provider_error"})
		return reason(SemanticUnavailableReasonProviderError)
	}
	_ = ai.CloseUsage(context.WithoutCancel(ctx), s.deps.Pool, usageID, ai.Outcome{Reported: res.Units, ProviderRequestID: res.RequestID})
	return vectorText(res.Vectors[0]), nil
}

func vectorText(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func toIndexState(st *search.State) SearchIndexState {
	return SearchIndexState{
		ActiveEmbeddingModel: st.ActiveModel, EmbeddingDimension: st.Dimension,
		ReindexingIntoModel: st.ReindexingInto, ReindexStartedAt: st.ReindexStartedAt,
		IncompleteNotes: st.IncompleteNotes, ChildrenNeedingEmbedding: st.ChildrenNeedingEmbedding,
	}
}

// GetSearchIndex is GET /settings/search-index.
func (s *Server) GetSearchIndex(ctx context.Context, _ GetSearchIndexRequestObject) (GetSearchIndexResponseObject, error) {
	st, err := search.ReadState(ctx, s.deps.Pool)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return GetSearchIndex200JSONResponse(toIndexState(st)), nil
}

// StartReindex is POST /settings/search-index/reindex. The embeddings role
// must already name the model with the index's dimension, since that is
// the key and the option the reindex job will call with.
func (s *Server) StartReindex(ctx context.Context, req StartReindexRequestObject) (StartReindexResponseObject, error) {
	session := SessionFrom(ctx)
	model := strings.TrimSpace(req.Body.Model)
	role, err := s.deps.AI.Get(ctx, session.UserID, ai.RoleEmbeddings)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if role.Model != model {
		return nil, apierr.New(apierr.InvalidRequest, "Set the embeddings role to "+model+" in Settings → AI first; the reindex calls with that role's key and options.")
	}
	var dimension int
	if err := s.deps.Pool.QueryRow(ctx, `SELECT embedding_dimension FROM search_index_state`).Scan(&dimension); err != nil {
		return nil, apierr.Internal(err)
	}
	if got, ok := optionInt(role.RequestOptions, "dimensions"); !ok || got != dimension {
		return nil, apierr.New(apierr.DimensionMismatch, "The index stores "+strconv.Itoa(dimension)+"-dimensional vectors. Set dimensions to "+strconv.Itoa(dimension)+" on the embeddings role, or change the column by migration.").With("embedding_dimension", dimension)
	}
	st, err := search.StartReindex(ctx, s.deps.Pool, model)
	switch {
	case errors.Is(err, search.ErrReindexInProgress):
		return nil, apierr.New(apierr.ReindexInProgress, "A reindex is already in progress. Activate or abandon it first.")
	case errors.Is(err, search.ErrSameModel):
		return nil, apierr.New(apierr.InvalidRequest, "Search already reads "+model+".")
	case err != nil:
		return nil, apierr.Internal(err)
	}
	if err := s.deps.Submissions.StartReindex(ctx, model); err != nil {
		return nil, apierr.Internal(err)
	}
	return StartReindex202JSONResponse(toIndexState(st)), nil
}

// AbandonReindex is POST /settings/search-index/abandon.
func (s *Server) AbandonReindex(ctx context.Context, _ AbandonReindexRequestObject) (AbandonReindexResponseObject, error) {
	st, err := search.AbandonReindex(ctx, s.deps.Pool)
	if errors.Is(err, search.ErrNoReindexInProgress) {
		return nil, apierr.New(apierr.NoReindexInProgress, "No reindex is in progress.")
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return AbandonReindex200JSONResponse(toIndexState(st)), nil
}

// ActivateReindex is POST /settings/search-index/activate: the gated flip.
func (s *Server) ActivateReindex(ctx context.Context, _ ActivateReindexRequestObject) (ActivateReindexResponseObject, error) {
	st, err := search.ActivateReindex(ctx, s.deps.Pool)
	var incomplete *search.IncompleteError
	switch {
	case errors.Is(err, search.ErrNoReindexInProgress):
		return nil, apierr.New(apierr.NoReindexInProgress, "No reindex is in progress.")
	case errors.As(err, &incomplete):
		return nil, apierr.New(apierr.ReindexIncomplete, strconv.Itoa(incomplete.Notes)+" notes still lack vectors under the incoming model. Wait for the reindex to finish.").With("incomplete_notes", incomplete.Notes)
	case err != nil:
		return nil, apierr.Internal(err)
	}
	return ActivateReindex200JSONResponse(toIndexState(st)), nil
}

func optionInt(options map[string]any, key string) (int, bool) {
	switch v := options[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}
