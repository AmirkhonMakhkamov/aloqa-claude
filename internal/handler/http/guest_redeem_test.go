package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/pkg/cerrors"
	guestsvc "aloqa/internal/service/guest"
)

type redeemTokenIssuer struct{}

func (redeemTokenIssuer) CreateSessionForUser(context.Context, uuid.UUID, string, string) (*guestsvc.TokenResult, error) {
	return &guestsvc.TokenResult{
		AccessToken:  "guest-access-token",
		RefreshToken: "guest-refresh-token",
		SessionID:    uuid.New().String(),
		ExpiresIn:    3600,
	}, nil
}

func newRedeemGuestRouter(workspaceID, callID uuid.UUID, callStatus entity.CallStatus) http.Handler {
	invites := &resolveInviteRepo{byToken: map[string]*entity.GuestInvite{
		"call-token": {
			ID:          uuid.New(),
			WorkspaceID: workspaceID,
			Token:       "call-token",
			ChannelIDs:  nil,
			CallID:      &callID,
			MaxUses:     100,
			Status:      entity.GuestInviteStatusActive,
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	}}
	guestService := guestsvc.NewService(
		invites,
		guestLinkGrantRepo{},
		guestLinkUserRepo{},
		&httpWorkspaceRepo{},
		newGuestLinkChannelRepo(nil, workspaceID),
		redeemTokenIssuer{},
	)
	guestService.SetCallLookup(resolveCallLookup{
		call: &entity.Call{ID: callID, WorkspaceID: workspaceID, Status: callStatus},
	})

	router := chi.NewRouter()
	router.Post("/api/v1/invites/{token}/redeem", NewGuestHandler(guestService).RedeemInvite)
	return router
}

func performRedeemRequest(router http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/invites/"+token+"/redeem",
		strings.NewReader(`{"display_name":"Guest User"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	return res
}

func TestRedeemInviteHTTPCallScopedEmptyChannelsSerializesArray(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	router := newRedeemGuestRouter(workspaceID, callID, entity.CallStatusActive)

	res := performRedeemRequest(router, "call-token")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), `"channel_ids":null`) {
		t.Fatalf("redeem response serialized channel_ids as null: %s", res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"channel_ids":[]`) {
		t.Fatalf("redeem response should serialize channel_ids as [], got: %s", res.Body.String())
	}

	var body struct {
		WorkspaceID uuid.UUID   `json:"workspace_id"`
		CallID      *uuid.UUID  `json:"call_id"`
		ChannelIDs  []uuid.UUID `json:"channel_ids"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode redeem response: %v (body=%s)", err, res.Body.String())
	}
	if body.WorkspaceID != workspaceID {
		t.Fatalf("workspace_id = %s, want %s", body.WorkspaceID, workspaceID)
	}
	if body.CallID == nil || *body.CallID != callID {
		t.Fatalf("call_id = %v, want %s", body.CallID, callID)
	}
	if body.ChannelIDs == nil {
		t.Fatalf("channel_ids decoded as nil; response must carry a JSON array")
	}
	if len(body.ChannelIDs) != 0 {
		t.Fatalf("channel_ids = %v, want empty for call-scoped invite", body.ChannelIDs)
	}
}

func TestRedeemInviteHTTPEndedCallReturnsCallEndedCode(t *testing.T) {
	workspaceID := uuid.New()
	callID := uuid.New()
	router := newRedeemGuestRouter(workspaceID, callID, entity.CallStatusEnded)

	res := performRedeemRequest(router, "call-token")
	if res.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410, body=%s", res.Code, res.Body.String())
	}

	var body errorBody
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, res.Body.String())
	}
	if body.Error.Code != string(cerrors.CodeCallEnded) {
		t.Fatalf("error code = %s, want %s", body.Error.Code, cerrors.CodeCallEnded)
	}
}
