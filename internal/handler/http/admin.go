package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"aloqa/internal/domain/entity"
	"aloqa/internal/middleware"
	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/pkg/id"
	"aloqa/internal/pkg/pagination"
	"aloqa/internal/service/admin"
)

// AdminHandler handles workspace administration endpoints.
type AdminHandler struct {
	svc *admin.Service
}

// NewAdminHandler creates a new AdminHandler.
func NewAdminHandler(svc *admin.Service) *AdminHandler {
	return &AdminHandler{svc: svc}
}

func (h *AdminHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	p := paginationFromQuery(r)
	members, err := h.svc.ListMembers(r.Context(), workspaceID, actorID, p)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, members)
}

func (h *AdminHandler) InviteMember(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	member, err := h.svc.InviteMember(r.Context(), workspaceID, actorID, admin.InviteMemberInput{
		Email: req.Email,
		Role:  entity.WorkspaceRole(req.Role),
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, member)
}

func (h *AdminHandler) ReindexSearch(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	result, err := h.svc.ReindexSearch(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (h *AdminHandler) BackfillSearchUsers(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromQuery(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	result, err := h.svc.BackfillSearchUsers(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, result)
}

func (h *AdminHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	roles, err := h.svc.ListRoleDefinitions(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, roles)
}

func (h *AdminHandler) ListMediaNodes(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	nodes, err := h.svc.ListMediaNodes(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, nodes)
}

func (h *AdminHandler) MediaTopology(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	topology, err := h.svc.GetWorkspaceMediaTopology(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, topology)
}

func (h *AdminHandler) StorageRuntime(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	report, err := h.svc.GetStorageRuntimeReport(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, report)
}

func (h *AdminHandler) StorageAudit(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	report, err := h.svc.GetStorageAudit(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, report)
}

func (h *AdminHandler) ObservabilityDashboard(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	report, err := h.svc.GetObservabilityDashboard(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, report)
}

func (h *AdminHandler) ObservabilityAlerts(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	alerts, err := h.svc.GetObservabilityAlerts(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, alerts)
}

func (h *AdminHandler) ObservabilitySLOs(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	slos, err := h.svc.GetObservabilitySLOs(r.Context(), workspaceID, actorID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, slos)
}

func (h *AdminHandler) CallQoSHistory(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed <= 0 {
			writeErr(w, cerrors.InvalidInput("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	if limit > 1000 {
		limit = 1000
	}

	history, err := h.svc.GetCallQoSHistory(r.Context(), workspaceID, actorID, callID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, history)
}

func (h *AdminHandler) CallQualityReport(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed <= 0 {
			writeErr(w, cerrors.InvalidInput("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	if limit > 1000 {
		limit = 1000
	}

	report, err := h.svc.GetCallQualityReport(r.Context(), workspaceID, actorID, callID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, report)
}

func (h *AdminHandler) GetCallQualityPolicy(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	policy, err := h.svc.GetCallQualityPolicy(r.Context(), workspaceID, actorID, callID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, policy)
}

func (h *AdminHandler) UpdateCallQualityPolicy(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Mode                    entity.MediaQualityPolicyMode `json:"mode"`
		AlertPacketLossPct      float64                       `json:"alert_packet_loss_pct"`
		AlertJitterMs           float64                       `json:"alert_jitter_ms"`
		AlertRoundTripTimeMs    float64                       `json:"alert_round_trip_time_ms"`
		CorrelationTolerancePct float64                       `json:"correlation_tolerance_pct"`
		CorrelationToleranceMs  float64                       `json:"correlation_tolerance_ms"`
		ServerDrivenEnabled     bool                          `json:"server_driven_enabled"`
		ServerDrivenMinInterval int                           `json:"server_driven_min_interval_ms"`
		MeetingWideDowngrade    bool                          `json:"meeting_wide_downgrade"`
		AlertingEnabled         bool                          `json:"alerting_enabled"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	policy, err := h.svc.UpdateCallQualityPolicy(r.Context(), workspaceID, actorID, callID, entity.MediaQualityPolicy{
		Mode:                    req.Mode,
		AlertPacketLossPct:      req.AlertPacketLossPct,
		AlertJitterMs:           req.AlertJitterMs,
		AlertRoundTripTimeMs:    req.AlertRoundTripTimeMs,
		CorrelationTolerancePct: req.CorrelationTolerancePct,
		CorrelationToleranceMs:  req.CorrelationToleranceMs,
		ServerDrivenEnabled:     req.ServerDrivenEnabled,
		ServerDrivenMinInterval: req.ServerDrivenMinInterval,
		MeetingWideDowngrade:    req.MeetingWideDowngrade,
		AlertingEnabled:         req.AlertingEnabled,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, policy)
}

func (h *AdminHandler) ListCallQualityAlerts(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	callID, err := id.Parse(chi.URLParam(r, "callID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, convErr := strconv.Atoi(raw)
		if convErr != nil || parsed <= 0 {
			writeErr(w, cerrors.InvalidInput("limit must be a positive integer"))
			return
		}
		limit = parsed
	}
	if limit > 500 {
		limit = 500
	}

	alerts, err := h.svc.ListCallQualityAlerts(r.Context(), workspaceID, actorID, callID, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, alerts)
}

func (h *AdminHandler) CreateRole(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Name        string   `json:"name"`
		BaseRole    string   `json:"base_role"`
		Permissions []string `json:"permissions"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	role, err := h.svc.CreateRoleDefinition(r.Context(), workspaceID, actorID, admin.RoleDefinitionInput{
		Name:        req.Name,
		BaseRole:    entity.WorkspaceRole(req.BaseRole),
		Permissions: req.Permissions,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeCreated(w, role)
}

func (h *AdminHandler) UpdateRole(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roleID, err := id.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Name        string   `json:"name"`
		BaseRole    string   `json:"base_role"`
		Permissions []string `json:"permissions"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	role, err := h.svc.UpdateRoleDefinition(r.Context(), workspaceID, actorID, roleID, admin.RoleDefinitionInput{
		Name:        req.Name,
		BaseRole:    entity.WorkspaceRole(req.BaseRole),
		Permissions: req.Permissions,
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, role)
}

func (h *AdminHandler) DeleteRole(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roleID, err := id.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.DeleteRoleDefinition(r.Context(), workspaceID, actorID, roleID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) UpdateMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	role := entity.WorkspaceRole(req.Role)
	switch role {
	case entity.WorkspaceRoleOwner, entity.WorkspaceRoleAdmin, entity.WorkspaceRoleMember, entity.WorkspaceRoleGuest:
	default:
		writeErr(w, cerrors.InvalidInput("invalid role"))
		return
	}

	if err := h.svc.UpdateMemberRole(r.Context(), workspaceID, actorID, targetID, role); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) ListMemberRoles(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	roles, err := h.svc.ListMemberRoles(r.Context(), workspaceID, actorID, targetID)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, roles)
}

func (h *AdminHandler) AssignMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	roleID, err := id.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.AssignMemberRole(r.Context(), workspaceID, actorID, targetID, roleID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) UnassignMemberRole(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	roleID, err := id.Parse(chi.URLParam(r, "roleID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.UnassignMemberRole(r.Context(), workspaceID, actorID, targetID, roleID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.RemoveMember(r.Context(), workspaceID, actorID, targetID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) SuspendUser(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.SuspendUser(r.Context(), workspaceID, actorID, targetID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) ReactivateUser(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	targetID, err := id.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	if err := h.svc.ReactivateUser(r.Context(), workspaceID, actorID, targetID); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) UpdateWorkspace(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	var req struct {
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	if err := h.svc.UpdateWorkspace(r.Context(), workspaceID, actorID, req.Name, req.AvatarURL); err != nil {
		writeErr(w, err)
		return
	}

	writeNoContent(w)
}

func (h *AdminHandler) AuditLog(w http.ResponseWriter, r *http.Request) {
	workspaceID, err := workspaceIDFromRequest(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	actorID := middleware.UserIDFromContext(r.Context())

	p := paginationFromQuery(r)
	entries, total, err := h.svc.GetAuditLog(r.Context(), workspaceID, actorID, p)
	if err != nil {
		writeErr(w, err)
		return
	}

	writeOK(w, map[string]any{
		"entries": entries,
		"total":   total,
	})
}

func workspaceIDFromRequest(r *http.Request) (uuid.UUID, error) {
	if workspaceID := middleware.WorkspaceIDFromContext(r.Context()); workspaceID != uuid.Nil {
		return workspaceID, nil
	}
	return id.Parse(chi.URLParam(r, "workspaceID"))
}

func workspaceIDFromQuery(r *http.Request) (uuid.UUID, error) {
	value := r.URL.Query().Get("workspace_id")
	if value == "" {
		return uuid.Nil, cerrors.InvalidInput("workspace_id query parameter is required")
	}
	workspaceID, err := id.Parse(value)
	if err != nil {
		return uuid.Nil, cerrors.InvalidInput("invalid workspace_id")
	}
	return workspaceID, nil
}

func paginationFromQuery(r *http.Request) pagination.Params {
	p := pagination.Params{Limit: 50}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			p.Limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}
	return p
}
