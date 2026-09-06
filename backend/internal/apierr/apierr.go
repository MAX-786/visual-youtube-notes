// Package apierr is the error envelope every non-2xx response carries
// (ADR 0015): one shape, a closed set of typed codes, an HTTP status fixed per
// code, a message written for a person that names the next action, and details
// whose keys are fixed per code.
package apierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Code is a typed reason from the contract's ErrorCode enum.
type Code string

// The closed set of codes. Adding one is a contract change: it must appear in
// docs/design/openapi.yaml's ErrorCode enum and status table first.
const (
	InvalidRequest              Code = "invalid_request"
	MixedTransport              Code = "mixed_transport"
	Unauthenticated             Code = "unauthenticated"
	CSRFRejected                Code = "csrf_rejected"
	OriginRejected              Code = "origin_rejected"
	PairingRejected             Code = "pairing_rejected"
	NotFound                    Code = "not_found"
	NotePurged                  Code = "note_purged"
	NoteTrashed                 Code = "note_trashed"
	NoteNotTrashed              Code = "note_not_trashed"
	NoteNotReady                Code = "note_not_ready"
	NoteNotFailed               Code = "note_not_failed"
	RunInFlight                 Code = "run_in_flight"
	RevisionNotActive           Code = "revision_not_active"
	RevisionNotAwaitingDecision Code = "revision_not_awaiting_decision"
	RegenerationInFlight        Code = "regeneration_in_flight"
	TakeawayStale               Code = "takeaway_stale"
	RolesUnconfigured           Code = "roles_unconfigured"
	NoActiveRevision            Code = "no_active_revision"
	CostHoldAbsent              Code = "cost_hold_absent"
	TopicInUse                  Code = "topic_in_use"
	TopicExists                 Code = "topic_exists"
	TagExists                   Code = "tag_exists"
	PairingNotApproved          Code = "pairing_not_approved"
	PairingDecided              Code = "pairing_decided"
	ReindexInProgress           Code = "reindex_in_progress"
	NoReindexInProgress         Code = "no_reindex_in_progress"
	ReindexIncomplete           Code = "reindex_incomplete"
	DimensionMismatch           Code = "dimension_mismatch"
	PairingExpired              Code = "pairing_expired"
	PairingDenied               Code = "pairing_denied"
	MutationExpired             Code = "mutation_expired"
	URLUnsupported              Code = "url_unsupported"
	ValidationFailed            Code = "validation_failed"
	InvalidRequestOptions       Code = "invalid_request_options"
	EndpointNotAllowed          Code = "endpoint_not_allowed"
	RateLimited                 Code = "rate_limited"
	InternalError               Code = "internal_error"
	RendererUnavailable         Code = "renderer_unavailable"
)

var statuses = map[Code]int{
	InvalidRequest:              http.StatusBadRequest,
	MixedTransport:              http.StatusBadRequest,
	Unauthenticated:             http.StatusUnauthorized,
	CSRFRejected:                http.StatusForbidden,
	OriginRejected:              http.StatusForbidden,
	PairingRejected:             http.StatusForbidden,
	NotFound:                    http.StatusNotFound,
	NotePurged:                  http.StatusNotFound,
	NoteTrashed:                 http.StatusConflict,
	NoteNotTrashed:              http.StatusConflict,
	NoteNotReady:                http.StatusConflict,
	NoteNotFailed:               http.StatusConflict,
	RunInFlight:                 http.StatusConflict,
	RevisionNotActive:           http.StatusConflict,
	RevisionNotAwaitingDecision: http.StatusConflict,
	RegenerationInFlight:        http.StatusConflict,
	TakeawayStale:               http.StatusConflict,
	RolesUnconfigured:           http.StatusConflict,
	NoActiveRevision:            http.StatusConflict,
	CostHoldAbsent:              http.StatusConflict,
	TopicInUse:                  http.StatusConflict,
	TopicExists:                 http.StatusConflict,
	TagExists:                   http.StatusConflict,
	PairingNotApproved:          http.StatusConflict,
	PairingDecided:              http.StatusConflict,
	ReindexInProgress:           http.StatusConflict,
	NoReindexInProgress:         http.StatusConflict,
	ReindexIncomplete:           http.StatusConflict,
	DimensionMismatch:           http.StatusConflict,
	PairingExpired:              http.StatusGone,
	PairingDenied:               http.StatusGone,
	MutationExpired:             http.StatusGone,
	URLUnsupported:              http.StatusUnprocessableEntity,
	ValidationFailed:            http.StatusUnprocessableEntity,
	InvalidRequestOptions:       http.StatusUnprocessableEntity,
	EndpointNotAllowed:          http.StatusUnprocessableEntity,
	RateLimited:                 http.StatusTooManyRequests,
	InternalError:               http.StatusInternalServerError,
	RendererUnavailable:         http.StatusBadGateway,
}

// Status is the HTTP status fixed for a code by the contract.
func Status(code Code) int {
	if s, ok := statuses[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// Error is one typed failure, carried as a Go error until it is written.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
	cause   error
}

// New builds an error with a message written for a person.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf builds an error with a formatted message.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// With adds one details key. The keys allowed per code are fixed by the
// contract's ErrorCode table.
func (e *Error) With(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// Wrap records the underlying cause, which is logged and never sent.
func (e *Error) Wrap(err error) *Error {
	e.cause = err
	return e
}

// Error implements error.
func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.cause }

// Status is the HTTP status for this error.
func (e *Error) Status() int { return Status(e.Code) }

// From converts any error into an envelope-bearing error. Anything that is not
// already typed becomes internal_error with a generic message; the cause stays
// attached for the log line and never reaches the client.
func From(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	return New(InternalError, "Something went wrong on the server. Try again, and check Settings → Health if it persists.").Wrap(err)
}

// Internal wraps an unexpected error.
func Internal(err error) *Error {
	return From(fmt.Errorf("internal: %w", err))
}

type envelope struct {
	Error body `json:"error"`
}

type body struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Write sends the envelope with the code's status. Headers that depend on the
// code, such as Retry-After, are the caller's to set before writing.
func Write(w http.ResponseWriter, err *Error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(err.Status())
	_ = json.NewEncoder(w).Encode(envelope{Error: body{Code: err.Code, Message: err.Message, Details: err.Details}})
}
