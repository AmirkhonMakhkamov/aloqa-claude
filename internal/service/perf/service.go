// Package perf ingests performance telemetry (epic ALK-849, spec §11): lab
// profiling runs produced by CI and sampled field RUM events forwarded by the FE
// BFF. The service owns the persistence orchestration plus the BACKEND privacy
// guard — a defense-in-depth re-validation that drops any field event still
// carrying a concrete id or raw URL before it can reach Postgres. The FE BFF is
// the primary normalizer; this layer assumes nothing about it.
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
	// UpsertLabRun inserts or updates a run by its identity tuple and returns the
	// run id (existing id on conflict).
	UpsertLabRun(ctx context.Context, run LabRun) (uuid.UUID, error)
	// UpsertLabMetrics inserts or updates the metric rows for a run.
	UpsertLabMetrics(ctx context.Context, runID uuid.UUID, metrics []LabMetric) error
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
		runID, err := s.repo.UpsertLabRun(ctx, runs[i])
		if err != nil {
			return err
		}
		if err := s.repo.UpsertLabMetrics(ctx, runID, runs[i].Metrics); err != nil {
			return err
		}
	}
	return nil
}

// IngestRum sanitizes the batch (dropping — not rejecting — any event still
// carrying a concrete id), stamps a server-side timestamp + 10s dedupe bucket,
// and persists the survivors. Returns the inserted and dropped counts.
func (s *Service) IngestRum(ctx context.Context, batch RumBatch) (inserted, dropped int, err error) {
	now := s.now()
	bucket := now.Unix() / rumDedupeBucketSeconds

	rows := make([]RumEventRecord, 0, len(batch.Events))
	for _, ev := range batch.Events {
		if !isCleanEvent(ev) {
			dropped++
			continue
		}
		rows = append(rows, RumEventRecord{
			Session:       batch.Session,
			RouteTemplate: ev.RouteTemplate,
			MetricKey:     ev.Name,
			Value:         ev.Value,
			Release:       batch.Release,
			DeviceClass:   batch.DeviceClass,
			Connection:    batch.Connection,
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
	// A UUID anywhere is a concrete id leak.
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// A long digit run catches numeric ids / epoch timestamps. Real route
	// segments and metric names never contain five consecutive digits.
	longDigitRunRe = regexp.MustCompile(`[0-9]{5,}`)
	// The FE only ever emits lowercase templates of `/`-joined segments where each
	// segment is a static kebab word or a `:placeholder`. Anything else (uppercase,
	// query chars, `.`, `%`, raw-URL punctuation) is a leak shape.
	allowedRouteRe = regexp.MustCompile(`^[a-z0-9:/_-]+$`)
	cleanSegmentRe = regexp.MustCompile(`^(:[a-z]+|[a-z0-9][a-z0-9-]*)$`)
	metricNameRe   = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)
)

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
	if !allowedRouteRe.MatchString(rt) {
		return false
	}
	if uuidRe.MatchString(rt) || longDigitRunRe.MatchString(rt) {
		return false
	}
	for _, seg := range strings.Split(rt, "/") {
		if seg == "" {
			continue // leading slash / root path
		}
		if len(seg) > maxSegmentLen || !cleanSegmentRe.MatchString(seg) {
			return false
		}
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
