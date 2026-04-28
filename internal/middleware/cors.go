package middleware

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig holds CORS middleware configuration.
type CORSConfig struct {
	AllowedOrigins   []string // Allowed origins. Use ["*"] for open access (dev only).
	AllowedMethods   []string // HTTP methods allowed. Defaults to common methods.
	AllowedHeaders   []string // Request headers the client may send.
	ExposedHeaders   []string // Response headers the client can read.
	AllowCredentials bool     // Whether to allow cookies/auth headers.
	MaxAge           int      // Preflight cache duration in seconds.
}

// DefaultCORSConfig returns a sensible default CORS configuration.
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: false,
		MaxAge:           86400,
	}
}

// CORS returns a middleware that handles Cross-Origin Resource Sharing.
//
// Security: combining AllowCredentials with a wildcard origin list would let
// any site issue authenticated requests, which is effectively the same as
// disabling CSRF protection. When AllowCredentials=true we silently drop the
// wildcard and require an explicit origin allow-list.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	allowedOrigins := make(map[string]bool, len(cfg.AllowedOrigins))
	allowAll := false
	for _, o := range cfg.AllowedOrigins {
		if o == "*" {
			if cfg.AllowCredentials {
				continue
			}
			allowAll = true
		}
		allowedOrigins[o] = true
	}

	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	exposed := strings.Join(cfg.ExposedHeaders, ", ")
	maxAge := strconv.Itoa(cfg.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			if origin != "" {
				allowed := allowedOrigins[origin]
				switch {
				case allowed:
					w.Header().Set("Access-Control-Allow-Origin", origin)
				case allowAll:
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}

				if cfg.AllowCredentials && allowed {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}

				if exposed != "" {
					w.Header().Set("Access-Control-Expose-Headers", exposed)
				}
			}

			// Handle preflight OPTIONS requests.
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				if cfg.MaxAge > 0 {
					w.Header().Set("Access-Control-Max-Age", maxAge)
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
