package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const unsignedPayloadHash = "UNSIGNED-PAYLOAD"

type S3Config struct {
	Endpoint            string
	Region              string
	Bucket              string
	AccessKey           string
	SecretKey           string
	UseSSL              bool
	ForcePathStyle      bool
	Prefix              string
	HTTPClient          *http.Client
	HotStorageClass     string
	WarmStorageClass    string
	ArchiveStorageClass string
}

type S3Storage struct {
	endpoint            *url.URL
	region              string
	bucket              string
	accessKey           string
	secretKey           string
	forcePathStyle      bool
	prefix              string
	client              *http.Client
	hotStorageClass     string
	warmStorageClass    string
	archiveStorageClass string
}

func NewS3Storage(cfg S3Config) (*S3Storage, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("s3 storage endpoint is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("s3 storage bucket is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, fmt.Errorf("s3 storage credentials are required")
	}
	endpoint, err := normalizeS3Endpoint(cfg.Endpoint, cfg.UseSSL)
	if err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "us-east-1"
	}
	return &S3Storage{
		endpoint:            endpoint,
		region:              region,
		bucket:              cfg.Bucket,
		accessKey:           cfg.AccessKey,
		secretKey:           cfg.SecretKey,
		forcePathStyle:      cfg.ForcePathStyle,
		prefix:              strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
		client:              client,
		hotStorageClass:     firstNonEmpty(cfg.HotStorageClass, "STANDARD"),
		warmStorageClass:    firstNonEmpty(cfg.WarmStorageClass, "STANDARD_IA"),
		archiveStorageClass: firstNonEmpty(cfg.ArchiveStorageClass, "GLACIER_IR"),
	}, nil
}

func (s *S3Storage) Put(ctx context.Context, key string, reader io.Reader, size int64, mimeType string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key).String(), reader)
	if err != nil {
		return err
	}
	req.ContentLength = size
	if mimeType != "" {
		req.Header.Set("Content-Type", mimeType)
	}
	if storageClass := s.DefaultStorageClass(ObjectTierHot); storageClass != "" {
		req.Header.Set("x-amz-storage-class", storageClass)
	}
	resp, err := s.doSigned(req, unsignedPayloadHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := s.expectOK(resp, http.StatusOK, http.StatusCreated); err != nil {
		return err
	}
	return nil
}

func (s *S3Storage) Get(ctx context.Context, key string) (io.ReadCloser, *FileInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.objectURL(key).String(), nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := s.doSigned(req, unsignedPayloadHash)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if err := s.expectOK(resp, http.StatusOK); err != nil {
		resp.Body.Close()
		return nil, nil, err
	}
	return resp.Body, fileInfoFromResponse(key, resp), nil
}

func (s *S3Storage) Delete(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.objectURL(key).String(), nil)
	if err != nil {
		return err
	}
	resp, err := s.doSigned(req, unsignedPayloadHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return s.expectOK(resp, http.StatusNoContent, http.StatusOK, http.StatusAccepted, http.StatusNotFound)
}

func (s *S3Storage) Exists(ctx context.Context, key string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, s.objectURL(key).String(), nil)
	if err != nil {
		return false, err
	}
	resp, err := s.doSigned(req, unsignedPayloadHash)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, s.expectOK(resp, http.StatusOK, http.StatusNotFound)
	}
}

func (s *S3Storage) SignedDownloadURL(_ context.Context, key string, opts SignedURLOptions) (string, error) {
	if opts.ExpiresIn <= 0 {
		opts.ExpiresIn = 15 * time.Minute
	}
	if opts.ExpiresIn > 7*24*time.Hour {
		opts.ExpiresIn = 7 * 24 * time.Hour
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	scope := shortDate + "/" + s.region + "/s3/aws4_request"
	objectURL := s.objectURL(key)

	query := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    s.accessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.FormatInt(int64(opts.ExpiresIn/time.Second), 10),
		"X-Amz-SignedHeaders": "host",
	}
	if opts.ContentType != "" {
		query["response-content-type"] = opts.ContentType
	}
	if opts.Attachment {
		filename := strings.ReplaceAll(opts.Filename, `"`, "")
		if filename == "" {
			filename = path.Base(key)
		}
		query["response-content-disposition"] = fmt.Sprintf(`attachment; filename="%s"`, filename)
	}

	canonicalQuery := canonicalQueryString(query)
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		canonicalURI(objectURL.Path),
		canonicalQuery,
		"host:" + canonicalHost(objectURL),
		"",
		"host",
		unsignedPayloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	query["X-Amz-Signature"] = s.signature(shortDate, stringToSign)
	signedURL := *objectURL
	signedURL.RawQuery = canonicalQueryString(query)
	return signedURL.String(), nil
}

func (s *S3Storage) TransitionObject(ctx context.Context, key string, _ ObjectTier, storageClass string) error {
	if strings.TrimSpace(storageClass) == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key).String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-amz-copy-source", s.copySource(key))
	req.Header.Set("x-amz-metadata-directive", "COPY")
	req.Header.Set("x-amz-storage-class", storageClass)
	resp, err := s.doSigned(req, unsignedPayloadHash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	return s.expectOK(resp, http.StatusOK)
}

func (s *S3Storage) DefaultStorageClass(tier ObjectTier) string {
	switch tier {
	case ObjectTierWarm:
		return s.warmStorageClass
	case ObjectTierArchive:
		return s.archiveStorageClass
	default:
		return s.hotStorageClass
	}
}

const s3MaxRetries = 3

// s3Retryable returns true if the HTTP status indicates a transient failure
// that is safe to retry (throttling, server errors).
func s3Retryable(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests ||
		statusCode == http.StatusServiceUnavailable ||
		statusCode == http.StatusBadGateway ||
		statusCode == http.StatusGatewayTimeout ||
		statusCode == http.StatusInternalServerError
}

func (s *S3Storage) doSigned(req *http.Request, payloadHash string) (*http.Response, error) {
	if payloadHash == "" {
		payloadHash = unsignedPayloadHash
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	scope := shortDate + "/" + s.region + "/s3/aws4_request"

	req.Header.Set("Host", canonicalHost(req.URL))
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	signedHeaders := signedHeadersList(req.Header)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.Path),
		canonicalQueryString(queryValuesMap(req.URL.Query())),
		canonicalHeaders(req.Header, req.URL),
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := s.signature(shortDate, stringToSign)
	req.Header.Set("Authorization", strings.Join([]string{
		"AWS4-HMAC-SHA256 Credential=" + s.accessKey + "/" + scope,
		"SignedHeaders=" + signedHeaders,
		"Signature=" + signature,
	}, ", "))

	var resp *http.Response
	var lastErr error
	for attempt := 0; attempt <= s3MaxRetries; attempt++ {
		if attempt > 0 {
			// Re-sign the request since the date will have changed.
			now = time.Now().UTC()
			amzDate = now.Format("20060102T150405Z")
			shortDate = now.Format("20060102")
			scope = shortDate + "/" + s.region + "/s3/aws4_request"
			req.Header.Set("x-amz-date", amzDate)
			req.Header.Set("x-amz-content-sha256", payloadHash)

			signedHeaders = signedHeadersList(req.Header)
			canonicalRequest = strings.Join([]string{
				req.Method,
				canonicalURI(req.URL.Path),
				canonicalQueryString(queryValuesMap(req.URL.Query())),
				canonicalHeaders(req.Header, req.URL),
				signedHeaders,
				payloadHash,
			}, "\n")
			stringToSign = strings.Join([]string{
				"AWS4-HMAC-SHA256",
				amzDate,
				scope,
				hexSHA256([]byte(canonicalRequest)),
			}, "\n")
			signature = s.signature(shortDate, stringToSign)
			req.Header.Set("Authorization", strings.Join([]string{
				"AWS4-HMAC-SHA256 Credential=" + s.accessKey + "/" + scope,
				"SignedHeaders=" + signedHeaders,
				"Signature=" + signature,
			}, ", "))

			backoff := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
			if backoff > 5*time.Second {
				backoff = 5 * time.Second
			}
			slog.Warn("retrying S3 request", "method", req.Method, "url", req.URL.String(), "attempt", attempt, "backoff", backoff)
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(backoff):
			}
		}
		resp, lastErr = s.client.Do(req)
		if lastErr != nil {
			continue
		}
		if !s3Retryable(resp.StatusCode) {
			return resp, nil
		}
		// Drain body before retry.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		lastErr = fmt.Errorf("s3 retryable status %d", resp.StatusCode)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("s3 request failed after %d retries: %w", s3MaxRetries, lastErr)
	}
	return resp, nil
}

func (s *S3Storage) signature(shortDate, stringToSign string) string {
	kDate := hmacSHA256([]byte("AWS4"+s.secretKey), shortDate)
	kRegion := hmacSHA256(kDate, s.region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	return hex.EncodeToString(hmacSHA256(kSigning, stringToSign))
}

func (s *S3Storage) objectURL(key string) *url.URL {
	fullKey := s.objectKey(key)
	u := *s.endpoint
	if s.forcePathStyle || strings.Trim(u.Path, "/") != "" {
		u.Path = joinURLPath(u.Path, s.bucket, fullKey)
		return &u
	}
	u.Host = s.bucket + "." + u.Host
	u.Path = joinURLPath("", fullKey)
	return &u
}

func (s *S3Storage) objectKey(key string) string {
	key = strings.TrimLeft(key, "/")
	if s.prefix == "" {
		return key
	}
	return strings.TrimLeft(path.Join(s.prefix, key), "/")
}

func (s *S3Storage) copySource(key string) string {
	source := s.bucket + "/" + s.objectKey(key)
	parts := strings.Split(source, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func (s *S3Storage) expectOK(resp *http.Response, allowed ...int) error {
	for _, code := range allowed {
		if resp.StatusCode == code {
			return nil
		}
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return fmt.Errorf("s3 unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func fileInfoFromResponse(key string, resp *http.Response) *FileInfo {
	info := &FileInfo{
		Key:          key,
		MimeType:     resp.Header.Get("Content-Type"),
		ETag:         strings.Trim(resp.Header.Get("ETag"), `"`),
		StorageClass: firstNonEmpty(resp.Header.Get("x-amz-storage-class"), "STANDARD"),
	}
	if size, err := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64); err == nil {
		info.Size = size
	}
	if lastModified := resp.Header.Get("Last-Modified"); lastModified != "" {
		if parsed, err := http.ParseTime(lastModified); err == nil {
			info.CreatedAt = parsed.UTC()
		}
	}
	return info
}

func normalizeS3Endpoint(raw string, useSSL bool) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("s3 endpoint is required")
	}
	if !strings.Contains(raw, "://") {
		scheme := "https"
		if !useSSL {
			scheme = "http"
		}
		raw = scheme + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse s3 endpoint: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid s3 endpoint: host is required")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func canonicalHost(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Host
}

func canonicalURI(rawPath string) string {
	if rawPath == "" {
		return "/"
	}
	parts := strings.Split(strings.TrimPrefix(rawPath, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/" + strings.Join(parts, "/")
}

func canonicalHeaders(header http.Header, requestURL *url.URL) string {
	keys := make([]string, 0, len(header)+1)
	seen := map[string]struct{}{}
	for key := range header {
		lower := strings.ToLower(key)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		keys = append(keys, lower)
	}
	if _, ok := seen["host"]; !ok {
		keys = append(keys, "host")
	}
	sort.Strings(keys)
	var builder strings.Builder
	for _, key := range keys {
		var value string
		if key == "host" {
			value = canonicalHost(requestURL)
		} else {
			value = strings.Join(header.Values(key), ",")
		}
		builder.WriteString(key)
		builder.WriteByte(':')
		builder.WriteString(strings.Join(strings.Fields(value), " "))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func signedHeadersList(header http.Header) string {
	keys := make([]string, 0, len(header)+1)
	seen := map[string]struct{}{}
	for key := range header {
		lower := strings.ToLower(key)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		keys = append(keys, lower)
	}
	if _, ok := seen["host"]; !ok {
		keys = append(keys, "host")
	}
	sort.Strings(keys)
	return strings.Join(keys, ";")
}

func canonicalQueryString(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, awsEncode(key)+"="+awsEncode(values[key]))
	}
	return strings.Join(parts, "&")
}

func queryValuesMap(values url.Values) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, val := range values {
		out[key] = strings.Join(val, ",")
	}
	return out
}

func awsEncode(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "*", "%2A")
	return strings.ReplaceAll(escaped, "%7E", "~")
}

func joinURLPath(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		cleaned = append(cleaned, strings.Trim(part, "/"))
	}
	if len(cleaned) == 0 {
		return "/"
	}
	return "/" + strings.Join(cleaned, "/")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hexSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

var _ Storage = (*S3Storage)(nil)
var _ DownloadSigner = (*S3Storage)(nil)
var _ TieredStorage = (*S3Storage)(nil)
