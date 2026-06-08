// Package perf ingests performance telemetry (epic ALK-849, spec §11): lab
// profiling runs produced by CI and sampled field RUM events forwarded by the FE
// BFF. The service owns the persistence orchestration plus the BACKEND privacy
// guard — a defense-in-depth re-validation that drops any field event (and
// scrubs any batch metadata) still carrying a concrete id or raw URL before it
// can reach Postgres. The FE BFF is the primary normalizer; this layer assumes
// nothing about it.
//
// The guard is STRUCTURAL: it accepts the FE contract (route templates are
// `/`-joined lowercase static words plus camelCase `:placeholder` segments such
// as `:wsId`/`:callId`; metric keys are dotted namespaces like `web-vital.lcp`)
// and rejects anything id-shaped (UUID,
// pure-numeric or long-digit-run segment, all-hex token, raw URL, query string,
// uppercase). It does not import the FE route/landmark registry, so it cannot
// prove a value is in the catalog — it can only prove a value is NOT a gross
// leak shape. That is the intended defense-in-depth boundary.
package perf

import (
	"context"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LabRun is one lab profiling run (one scenario × profile × environment) parsed
// from the CI result.json.
type LabRun struct {
	Commit      string
	Branch      string
	Profile     string
	Environment string
	Scenario    string
	TS          time.Time
	CIRunURL    string
	Metrics     []LabMetric
}

// LabMetric is a single measured metric within a run.
type LabMetric struct {
	Key           string
	RouteTemplate string
	Case          string
	Value         float64
	Unit          string
}

// RumBatch is one sampled field beacon forwarded by the FE BFF.
type RumBatch struct {
	Session     string
	Release     string
	DeviceClass string
	Connection  string
	Events      []RumEvent
}

// RumEvent is a single web-vital or landmark sample.
type RumEvent struct {
	Kind          string // "web-vital" | "landmark"
	Name          string // metric key, e.g. "web-vital.lcp" or a landmark name
	RouteTemplate string
	Value         float64
}

// RumEventRecord is a sanitized field event ready to persist.
type RumEventRecord struct {
	Session       string
	RouteTemplate string
	MetricKey     string
	Value         float64
	Release       string
	DeviceClass   string
	Connection    string
	TS            time.Time
	TSBucket      int64
}

// Repository is the persistence port (implemented by repository/postgres).
type Repository interface {
	// UpsertLabRunWithMetrics upserts a run by its identity tuple together with its
	// metric rows in a single transaction, returning the run id (existing id on
	// conflict).
	UpsertLabRunWithMetrics(ctx context.Context, run LabRun) (uuid.UUID, error)
	// InsertRumEvents inserts sanitized field events, ignoring 10s-bucket dupes.
	InsertRumEvents(ctx context.Context, rows []RumEventRecord) error
}

const (
	// rumDedupeBucketSeconds groups events into 10s windows for dedupe (spec §11).
	rumDedupeBucketSeconds = 10
	maxRouteTemplateLen    = 200
	maxMetricKeyLen        = 120
	maxSegmentLen          = 40
)

// Service orchestrates persistence and applies the backend privacy guard.
type Service struct {
	repo Repository
	now  func() time.Time
}

// Option customizes a Service.
type Option func(*Service)

// WithClock overrides the time source (used in tests for deterministic buckets).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService builds a Service backed by repo.
func NewService(repo Repository, opts ...Option) *Service {
	s := &Service{repo: repo, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// IngestLab persists each run and its metrics. Runs are trusted CI output (our
// own route templates, no user data), so they are not privacy-filtered.
func (s *Service) IngestLab(ctx context.Context, runs []LabRun) error {
	for i := range runs {
		if _, err := s.repo.UpsertLabRunWithMetrics(ctx, runs[i]); err != nil {
			return err
		}
	}
	return nil
}

// IngestRum scrubs the batch metadata, drops — not rejects — any event still
// carrying a concrete id, stamps a server-side timestamp + 10s dedupe bucket, and
// persists the survivors. Returns the inserted and dropped counts. A batch whose
// session id is not an opaque token is dropped whole (every event counted as
// dropped) — a malformed session is the one field that taints the entire batch.
func (s *Service) IngestRum(ctx context.Context, batch RumBatch) (inserted, dropped int, err error) {
	meta, ok := sanitizeBatchMeta(batch)
	if !ok {
		return 0, len(batch.Events), nil
	}

	now := s.now()
	bucket := now.Unix() / rumDedupeBucketSeconds

	rows := make([]RumEventRecord, 0, len(batch.Events))
	for _, ev := range batch.Events {
		if !isCleanEvent(ev) {
			dropped++
			continue
		}
		rows = append(rows, RumEventRecord{
			Session:       meta.session,
			RouteTemplate: ev.RouteTemplate,
			MetricKey:     ev.Name,
			Value:         ev.Value,
			Release:       meta.release,
			DeviceClass:   meta.deviceClass,
			Connection:    meta.connection,
			TS:            now,
			TSBucket:      bucket,
		})
	}

	if len(rows) == 0 {
		return 0, dropped, nil
	}
	if err := s.repo.InsertRumEvents(ctx, rows); err != nil {
		return 0, dropped, err
	}
	return len(rows), dropped, nil
}

// --- backend privacy guard (defense in depth) ---------------------------------

var (
	// A UUID anywhere is a concrete id leak (in a route/metric — NOT a session,
	// which is a legitimately UUID-shaped pseudonymous id, see sanitizeBatchMeta).
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// Three+ consecutive digits in a route/metric segment signal a numeric id or
	// epoch timestamp. Real route words and metric names never contain them.
	longDigitRunRe = regexp.MustCompile(`[0-9]{3,}`)
	// The FE emits `/`-joined segments: lowercase static words plus camelCase
	// `:placeholder` names (the route-template normalizer uses :wsId, :chId,
	// :dmId, :callId, :id, :token — see routeTemplate.ts PARAM_NAME_BY_PARENT).
	// Uppercase is therefore allowed in the charset, but only inside a
	// `:placeholder` (a FE-authored segment NAME with no user data); a STATIC
	// segment value must still be lowercase (staticSegRe), so an uppercase
	// concrete id value is still rejected.
	allowedRouteRe = regexp.MustCompile(`^[a-zA-Z0-9:/_-]+$`)
	placeholderRe  = regexp.MustCompile(`^:[a-zA-Z]+$`)
	staticSegRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	pureNumericRe  = regexp.MustCompile(`^[0-9]+$`)
	// An 8+ char all-hex segment is a hash/token-shaped id (catches non-UUID hex).
	hexTokenRe = regexp.MustCompile(`^[0-9a-f]{8,}$`)
	// Metric keys are dotted namespaces: web-vital.lcp, calendar.month-view.render.
	// Requiring at least one dot blocks a bare slug being used as a leak channel.
	metricNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z0-9-]+)+$`)
	// Session is an opaque token (crypto.randomUUID or s-<base36> on the FE): a
	// hyphen/underscore alnum charset that permits a UUID but rejects PII shapes
	// (emails, raw URLs, spaces).
	sessionRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// Release is a version/sha: alnum plus . + _ - (e.g. v0.25.0 or a git sha).
	releaseRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

// Coarse enums for device_class / connection — anything else is dropped to blank
// rather than stored, so a free-text value can't smuggle data in.
var (
	allowedDeviceClass = map[string]bool{"desktop": true, "tablet": true, "mobile": true}
	allowedConnection  = map[string]bool{
		"slow-2g": true, "2g": true, "3g": true, "4g": true, "5g": true,
		"wifi": true, "ethernet": true, "unknown": true, "none": true,
	}
)

type batchMeta struct {
	session     string
	release     string
	deviceClass string
	connection  string
}

// sanitizeBatchMeta validates the per-batch metadata that bypasses the per-event
// guard. A bad session taints the whole batch (ok=false); release/deviceClass/
// connection are coerced to blank when they fail rather than rejecting the batch.
func sanitizeBatchMeta(b RumBatch) (batchMeta, bool) {
	if !sessionRe.MatchString(b.Session) {
		return batchMeta{}, false
	}
	meta := batchMeta{session: b.Session}
	if releaseRe.MatchString(b.Release) {
		meta.release = b.Release
	}
	if allowedDeviceClass[b.DeviceClass] {
		meta.deviceClass = b.DeviceClass
	}
	if allowedConnection[b.Connection] {
		meta.connection = b.Connection
	}
	return meta, true
}

// isCleanEvent keeps an event only if its value is finite, its kind is known, and
// BOTH its route template and metric-key name are structurally clean.
func isCleanEvent(ev RumEvent) bool {
	if !isFinite(ev.Value) {
		return false
	}
	if ev.Kind != "web-vital" && ev.Kind != "landmark" {
		return false
	}
	return isCleanRouteTemplate(ev.RouteTemplate) && isCleanMetricName(ev.Name)
}

func isCleanRouteTemplate(rt string) bool {
	if rt == "" || len(rt) > maxRouteTemplateLen || rt[0] != '/' {
		return false
	}
	if !allowedRouteRe.MatchString(rt) || uuidRe.MatchString(rt) {
		return false
	}
	for _, seg := range strings.Split(rt, "/") {
		if seg == "" {
			continue // leading slash / root path
		}
		if !isCleanSegment(seg) {
			return false
		}
	}
	return true
}

// isCleanSegment accepts a `:placeholder` or a short static lowercase word, and
// rejects id-shaped literals: pure numeric, a 3+ digit run, or an 8+ char all-hex
// token. The FE turns every dynamic value into a `:placeholder`, so a literal
// id-shaped segment is by definition a leak.
func isCleanSegment(seg string) bool {
	if len(seg) > maxSegmentLen {
		return false
	}
	if strings.HasPrefix(seg, ":") {
		return placeholderRe.MatchString(seg)
	}
	if !staticSegRe.MatchString(seg) {
		return false
	}
	if pureNumericRe.MatchString(seg) || longDigitRunRe.MatchString(seg) || hexTokenRe.MatchString(seg) {
		return false
	}
	return true
}

func isCleanMetricName(name string) bool {
	if name == "" || len(name) > maxMetricKeyLen {
		return false
	}
	if uuidRe.MatchString(name) || longDigitRunRe.MatchString(name) {
		return false
	}
	return metricNameRe.MatchString(name)
}

func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
