package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"aloqa/internal/pkg/cerrors"
)

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	resp := errorResponse{}
	resp.Error.Code = http.StatusText(status)
	resp.Error.Message = msg
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		if cerrors.IsClientDisconnect(err) {
			slog.Debug("middleware error response encode aborted: client disconnected", "error", err)
			return
		}
		slog.Error("failed to encode middleware error response", "error", err)
	}
}
