package http

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"aloqa/internal/pkg/cerrors"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to encode response", "error", err)
	}
}

func writeOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, v)
}

func writeCreated(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusCreated, v)
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeErr(w http.ResponseWriter, err error) {
	if appErr, ok := cerrors.AsAppError(err); ok {
		writeJSON(w, appErr.HTTPStatus(), errorBody{
			Error: errorDetail{
				Code:    string(appErr.Code),
				Message: appErr.Message,
			},
		})
		return
	}

	slog.Error("unhandled error", "error", err)
	writeJSON(w, http.StatusInternalServerError, errorBody{
		Error: errorDetail{
			Code:    "INTERNAL",
			Message: "internal server error",
		},
	})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return cerrors.InvalidInput("request body is required")
		}
		return cerrors.InvalidInput("invalid request body")
	}

	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return cerrors.InvalidInput("request body must contain a single JSON value")
	}

	return nil
}
