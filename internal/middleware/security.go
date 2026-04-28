package middleware

import "net/http"

// SecureHeaders adds security-related HTTP response headers to every response.
// These mitigate common web attacks including XSS, clickjacking, MIME sniffing,
// and protocol downgrade attacks.
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()

		// Prevent MIME type sniffing.
		h.Set("X-Content-Type-Options", "nosniff")

		// Prevent clickjacking.
		h.Set("X-Frame-Options", "DENY")

		// Enable browser XSS filter (legacy but still useful).
		h.Set("X-XSS-Protection", "1; mode=block")

		// Enforce HTTPS via HSTS (1 year, include subdomains, allow preload).
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")

		// Content Security Policy: restrict resource loading.
		// API responses should never be rendered as HTML; this is defense-in-depth.
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")

		// Prevent cross-origin window references.
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		// Referrer policy: send origin only on cross-origin requests.
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Prevent caching of API responses containing sensitive data.
		h.Set("Cache-Control", "no-store")
		h.Set("Pragma", "no-cache")

		// Restrict browser features. Camera and microphone are not needed on
		// API responses; the frontend serves its own Permissions-Policy.
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")

		next.ServeHTTP(w, r)
	})
}
