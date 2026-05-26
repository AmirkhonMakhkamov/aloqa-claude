package call

import (
	"errors"
	"net/http"
	"testing"

	"github.com/twitchtv/twirp"

	"aloqa/internal/pkg/cerrors"
)

func TestMapTwirpErrorToAppError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		twirpCode  twirp.ErrorCode
		wantCode   cerrors.Code
		wantStatus int
	}{
		{"unavailable_to_503", twirp.Unavailable, cerrors.CodeUnavailable, http.StatusServiceUnavailable},
		{"deadline_to_503", twirp.DeadlineExceeded, cerrors.CodeUnavailable, http.StatusServiceUnavailable},
		{"invalid_arg_to_400", twirp.InvalidArgument, cerrors.CodeInvalidInput, http.StatusBadRequest},
		{"unauthenticated_to_500", twirp.Unauthenticated, cerrors.CodeInternal, http.StatusInternalServerError},
		{"permission_denied_to_500", twirp.PermissionDenied, cerrors.CodeInternal, http.StatusInternalServerError},
		{"internal_to_500", twirp.Internal, cerrors.CodeInternal, http.StatusInternalServerError},
		// Pin the default branch so future twirp codes
		// (NotFound, ResourceExhausted, Canceled, etc.) fall to CodeInternal/500.
		{"not_found_to_500_default", twirp.NotFound, cerrors.CodeInternal, http.StatusInternalServerError},
		{"resource_exhausted_to_500_default", twirp.ResourceExhausted, cerrors.CodeInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			twerr := twirp.NewError(tc.twirpCode, "lk says no")
			mapped := mapTwirpErrorToAppError(twerr, "failed to create livekit room")
			appErr, ok := cerrors.AsAppError(mapped)
			if !ok {
				t.Fatalf("mapTwirpErrorToAppError err = %T (%v), want *cerrors.AppError", mapped, mapped)
			}
			if appErr.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", appErr.Code, tc.wantCode)
			}
			if appErr.HTTPStatus() != tc.wantStatus {
				t.Fatalf("HTTPStatus = %d, want %d", appErr.HTTPStatus(), tc.wantStatus)
			}
		})
	}
}

func TestMapTwirpErrorToAppError_NonTwirp_IsInternal(t *testing.T) {
	t.Parallel()
	mapped := mapTwirpErrorToAppError(errors.New("connection refused"), "failed to create livekit room")
	appErr, ok := cerrors.AsAppError(mapped)
	if !ok {
		t.Fatalf("err = %T (%v), want *cerrors.AppError", mapped, mapped)
	}
	if appErr.Code != cerrors.CodeInternal {
		t.Fatalf("code = %q, want %q", appErr.Code, cerrors.CodeInternal)
	}
}
