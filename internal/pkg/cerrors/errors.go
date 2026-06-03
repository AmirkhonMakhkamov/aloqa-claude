package cerrors

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
)

// StatusClientClosedRequest is the non-standard 499 status (nginx convention)
// returned when the client closed the connection before the server finished.
// net/http has no constant for it. Because it is < 500 it stays out of the
// 5xx error-rate metric, and the request-log line for it is suppressed via the
// canceled request context (see internal/pkg/logging.SuppressCanceled).
const StatusClientClosedRequest = 499

// Code represents an application error code.
type Code string

const (
	CodeNotFound           Code = "NOT_FOUND"
	CodeAlreadyExists      Code = "ALREADY_EXISTS"
	CodeInvalidInput       Code = "INVALID_INPUT"
	CodeUnauthorized       Code = "UNAUTHORIZED"
	CodeForbidden          Code = "FORBIDDEN"
	CodeAccountDeactivated Code = "ACCOUNT_DEACTIVATED"
	CodeAccountSuspended   Code = "ACCOUNT_SUSPENDED"
	CodeNotDeactivated     Code = "NOT_DEACTIVATED"
	CodeInternal           Code = "INTERNAL"
	CodeConflict           Code = "CONFLICT"
	CodeRateLimited        Code = "RATE_LIMITED"
	CodeUnavailable        Code = "UNAVAILABLE"
	CodeUnprocessable      Code = "UNPROCESSABLE"
	CodeCallEnded          Code = "CALL_ENDED"
	CodeCanceled           Code = "CANCELED"
	// CodeWaitingRoomDeclined marks a join attempt by a participant the host has
	// already declined (ALK-700 guest flow). Maps to 403 so the FE deep-link
	// classifier can render a terminal "declined" state and stop re-polling.
	CodeWaitingRoomDeclined Code = "WAITING_ROOM_DECLINED"
	// CodeCallPasswordRequired marks a join rejected because the call's password
	// entry mode requires a password that was missing or incorrect (#4). Maps to
	// 401 with a distinct code so the FE can show a password prompt without
	// conflating it with a session-auth 401.
	CodeCallPasswordRequired Code = "CALL_PASSWORD_REQUIRED"
	// CodeChannelCallExists marks an attempt to start a call in a channel that
	// already has an active one (one call per channel). Maps to 409 so the FE
	// can join the existing call instead of starting a second.
	CodeChannelCallExists Code = "CHANNEL_ALREADY_HAS_ACTIVE_CALL"
	// CodeUserInCall marks an attempt to start a call while already connected to
	// another (a user may be in only one call at a time). Maps to 409.
	CodeUserInCall Code = "USER_ALREADY_IN_CALL"
)

// AppError is the standard application error type.
type AppError struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
	Err     error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.Err
}

// HTTPStatus maps error codes to HTTP status codes.
func (e *AppError) HTTPStatus() int {
	switch e.Code {
	case CodeNotFound:
		return http.StatusNotFound
	case CodeAlreadyExists, CodeConflict, CodeChannelCallExists, CodeUserInCall:
		return http.StatusConflict
	case CodeInvalidInput:
		return http.StatusBadRequest
	case CodeUnauthorized, CodeCallPasswordRequired:
		return http.StatusUnauthorized
	case CodeForbidden, CodeAccountDeactivated, CodeAccountSuspended, CodeNotDeactivated, CodeWaitingRoomDeclined:
		return http.StatusForbidden
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeUnprocessable:
		return http.StatusUnprocessableEntity
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeCallEnded:
		return http.StatusGone
	case CodeCanceled:
		return StatusClientClosedRequest
	default:
		return http.StatusInternalServerError
	}
}

func NotFound(msg string) *AppError {
	return &AppError{Code: CodeNotFound, Message: msg}
}

func AlreadyExists(msg string) *AppError {
	return &AppError{Code: CodeAlreadyExists, Message: msg}
}

func InvalidInput(msg string) *AppError {
	return &AppError{Code: CodeInvalidInput, Message: msg}
}

func Unauthorized(msg string) *AppError {
	return &AppError{Code: CodeUnauthorized, Message: msg}
}

func CallPasswordRequired(msg string) *AppError {
	return &AppError{Code: CodeCallPasswordRequired, Message: msg}
}

func Forbidden(msg string) *AppError {
	return &AppError{Code: CodeForbidden, Message: msg}
}

func AccountDeactivated() *AppError {
	return &AppError{Code: CodeAccountDeactivated, Message: "account is deactivated"}
}

func AccountSuspended() *AppError {
	return &AppError{Code: CodeAccountSuspended, Message: "account is suspended"}
}

func NotDeactivated() *AppError {
	return &AppError{Code: CodeNotDeactivated, Message: "account is not deactivated"}
}

func Internal(msg string, err error) *AppError {
	return &AppError{Code: CodeInternal, Message: msg, Err: err}
}

func Conflict(msg string) *AppError {
	return &AppError{Code: CodeConflict, Message: msg}
}

// ChannelCallExists returns a 409 marking a channel that already hosts an active
// call, so the FE joins the existing call instead of starting a second one.
func ChannelCallExists(msg string) *AppError {
	return &AppError{Code: CodeChannelCallExists, Message: msg}
}

// UserInCall returns a 409 marking an attempt to be in two calls at once.
func UserInCall(msg string) *AppError {
	return &AppError{Code: CodeUserInCall, Message: msg}
}

func Unavailable(msg string) *AppError {
	return &AppError{Code: CodeUnavailable, Message: msg}
}

func Unprocessable(msg string) *AppError {
	return &AppError{Code: CodeUnprocessable, Message: msg}
}

// CallEnded returns a typed AppError with HTTP 410 Gone status. Used by the call
// service when a JoinCall attempt targets a call that's already in CallStatusEnded,
// so the FE deep-link classifier can render an ended-call UX instead of a generic
// 403 retry spinner.
func CallEnded(msg string) *AppError {
	return &AppError{Code: CodeCallEnded, Message: msg}
}

// WaitingRoomDeclined returns a typed AppError with HTTP 403 status and the
// WAITING_ROOM_DECLINED code. Emitted when a (guest) participant the host has
// already declined attempts to re-join, so the FE renders a terminal declined
// state instead of re-polling the waiting room (ALK-700).
func WaitingRoomDeclined(msg string) *AppError {
	return &AppError{Code: CodeWaitingRoomDeclined, Message: msg}
}

// Canceled returns a typed AppError with HTTP 499 status, used when a request
// is abandoned because the client closed the connection.
func Canceled(msg string) *AppError {
	return &AppError{Code: CodeCanceled, Message: msg}
}

// IsContextCanceled reports whether err (or anything it wraps) is a
// context.Canceled. Used by the HTTP layer to distinguish a client disconnect
// (the parent request context was canceled) from a genuine server fault.
//
// context.DeadlineExceeded is deliberately NOT treated as a cancellation: it
// signals a server-side timeout (e.g. a slow or dead dependency) that we still
// want surfaced as a 5xx and alerted on.
func IsContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

// IsClientDisconnect reports whether err means the client went away: a canceled
// context, or a network write failure to a peer that has already gone (broken
// pipe, connection reset, closed connection). Used when encoding a response
// body so a disconnect mid-flush is not logged as a server ERROR. A genuine
// encode failure (e.g. an unmarshalable value) is not a disconnect and stays
// loud.
func IsClientDisconnect(err error) bool {
	if err == nil {
		return false
	}
	if IsContextCanceled(err) {
		return true
	}
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, net.ErrClosed)
}

// AsAppError extracts an *AppError from an error chain.
func AsAppError(err error) (*AppError, bool) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr, true
	}
	return nil, false
}
