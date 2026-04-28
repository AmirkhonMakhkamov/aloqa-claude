package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type IdempotencyConfig struct {
	TTL          time.Duration
	MaxBodyBytes int64
}

type idempotencyRecord struct {
	RequestHash string              `json:"request_hash"`
	StatusCode  int                 `json:"status_code"`
	Headers     map[string][]string `json:"headers"`
	Body        []byte              `json:"body"`
	StoredAt    time.Time           `json:"stored_at"`
}

type idempotencyWriter struct {
	headers http.Header
	body    bytes.Buffer
	status  int
}

func Idempotency(rdb *redis.Client, cfg IdempotencyConfig) func(http.Handler) http.Handler {
	if cfg.TTL <= 0 {
		cfg.TTL = 24 * time.Hour
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 1 << 20
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rdb == nil || !isIdempotentWriteMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}

			userID := UserIDFromContext(r.Context())
			if userID == uuid.Nil {
				next.ServeHTTP(w, r)
				return
			}

			requestHash, body, err := fingerprintRequest(r, cfg.MaxBodyBytes)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			storeKey := idempotencyKey(userID, r.Method, r.URL.Path, key)
			stored, err := rdb.Get(r.Context(), storeKey).Bytes()
			if err != nil && err != redis.Nil {
				slog.Error("failed to load idempotency record", "key", storeKey, "error", err)
				next.ServeHTTP(w, r)
				return
			}
			if err == nil {
				var record idempotencyRecord
				if unmarshalErr := json.Unmarshal(stored, &record); unmarshalErr == nil {
					if record.RequestHash != requestHash {
						writeError(w, http.StatusConflict, "idempotency key was already used with a different request")
						return
					}
					copyHeaders(w.Header(), record.Headers)
					w.Header().Set("X-Idempotency-Replayed", "true")
					w.WriteHeader(record.StatusCode)
					if len(record.Body) > 0 {
						_, _ = w.Write(record.Body)
					}
					return
				}
			}

			rec := &idempotencyWriter{headers: make(http.Header)}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			copyHeaders(w.Header(), rec.headers)
			w.WriteHeader(rec.status)
			if rec.body.Len() > 0 {
				_, _ = w.Write(rec.body.Bytes())
			}

			if rec.status >= 500 {
				return
			}

			record := idempotencyRecord{
				RequestHash: requestHash,
				StatusCode:  rec.status,
				Headers:     cloneHeaders(rec.headers),
				Body:        append([]byte(nil), rec.body.Bytes()...),
				StoredAt:    time.Now().UTC(),
			}
			payload, err := json.Marshal(record)
			if err != nil {
				slog.Error("failed to marshal idempotency record", "key", storeKey, "error", err)
				return
			}
			if err := rdb.Set(r.Context(), storeKey, payload, cfg.TTL).Err(); err != nil {
				slog.Error("failed to store idempotency record", "key", storeKey, "error", err)
			}
		})
	}
}

func (w *idempotencyWriter) Header() http.Header {
	return w.headers
}

func (w *idempotencyWriter) WriteHeader(statusCode int) {
	if w.status != 0 {
		return
	}
	w.status = statusCode
}

func (w *idempotencyWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(data)
}

func fingerprintRequest(r *http.Request, maxBodyBytes int64) (string, []byte, error) {
	body := []byte(nil)
	if r.Body != nil {
		limited := io.LimitReader(r.Body, maxBodyBytes+1)
		read, err := io.ReadAll(limited)
		if err != nil {
			return "", nil, err
		}
		body = read
	}
	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	hashInput := r.Method + "\n" + r.URL.Path + "\n" + r.URL.RawQuery + "\n" + contentType + "\n"
	if strings.HasPrefix(contentType, "multipart/form-data") || int64(len(body)) > maxBodyBytes {
		hashInput += "body-skipped"
	} else {
		sum := sha256.Sum256(body)
		hashInput += hex.EncodeToString(sum[:])
	}
	requestHash := sha256.Sum256([]byte(hashInput))
	return hex.EncodeToString(requestHash[:]), body, nil
}

func idempotencyKey(userID uuid.UUID, method, path, key string) string {
	return fmt.Sprintf("idempotency:%s:%s:%s:%s", userID, method, path, key)
}

func isIdempotentWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func copyHeaders(dst, src http.Header) {
	for key := range dst {
		delete(dst, key)
	}
	for key, values := range src {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cloneHeaders(src http.Header) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	cloned := make(map[string][]string, len(src))
	for key, values := range src {
		copied := make([]string, len(values))
		copy(copied, values)
		cloned[key] = copied
	}
	return cloned
}
