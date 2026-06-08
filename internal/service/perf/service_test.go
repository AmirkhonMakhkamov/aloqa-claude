package perf

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeRepo struct {
	runs       []LabRun
	runID      uuid.UUID
	metricsFor map[uuid.UUID][]LabMetric
	rumRows    []RumEventRecord
	insertErr  error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{runID: uuid.New(), metricsFor: map[uuid.UUID][]LabMetric{}}
}

func (f *fakeRepo) UpsertLabRun(_ context.Context, run LabRun) (uuid.UUID, error) {
	f.runs = append(f.runs, run)
	return f.runID, nil
}

func (f *fakeRepo) UpsertLabMetrics(_ context.Context, runID uuid.UUID, metrics []LabMetric) error {
	f.metricsFor[runID] = append(f.metricsFor[runID], metrics...)
	return nil
}

func (f *fakeRepo) InsertRumEvents(_ context.Context, rows []RumEventRecord) error {
	if f.insertErr != nil {
		return f.insertErr
	}
	f.rumRows = append(f.rumRows, rows...)
	return nil
}

func fixedClock(t time.Time) Option { return WithClock(func() time.Time { return t }) }

func TestIsCleanRouteTemplate(t *testing.T) {
	clean := []string{
		"/",
		"/login",
		"/w/:ws",
		"/w/:ws/c/:channel",
		"/w/:ws/calendar",
		"/join/:token",
		"/settings/account",
		"/guest",
	}
	for _, rt := range clean {
		if !isCleanRouteTemplate(rt) {
			t.Errorf("expected clean route template to pass: %q", rt)
		}
	}

	dirty := []string{
		"",      // empty
		"login", // no leading slash
		"/w/019e24ef-c454-7000-8000-0000000000aa", // uuid leak
		"/c/123456",                      // numeric id leak
		"/w/My-Workspace",                // uppercase (FE is all-lowercase)
		"https://airion-cargo.store/w/x", // raw URL
		"/search?q=secret",               // query string
		"/w/../etc",                      // traversal / dot
		"/w/ :channel",                   // whitespace
		"/w/" + string(make([]byte, 60)), // overlong segment (NUL bytes also fail charset)
	}
	for _, rt := range dirty {
		if isCleanRouteTemplate(rt) {
			t.Errorf("expected dirty route template to be rejected: %q", rt)
		}
	}
}

func TestIsCleanMetricName(t *testing.T) {
	clean := []string{"web-vital.lcp", "web-vital.cls", "calendar.month-view.render", "ai.submit-to-first-token"}
	for _, n := range clean {
		if !isCleanMetricName(n) {
			t.Errorf("expected clean metric name to pass: %q", n)
		}
	}
	dirty := []string{"", "Web-Vital.LCP", "channel-019e24ef-c454-7000-8000-0000000000aa", "msg-123456", "raw url"}
	for _, n := range dirty {
		if isCleanMetricName(n) {
			t.Errorf("expected dirty metric name to be rejected: %q", n)
		}
	}
}

func TestIngestRumDropsDirtyAndStampsBucket(t *testing.T) {
	repo := newFakeRepo()
	// 2026-06-08T12:00:05Z → unix 1781265605 → bucket /10 = 178126560.
	clock := time.Date(2026, 6, 8, 12, 0, 5, 0, time.UTC)
	svc := NewService(repo, fixedClock(clock))

	batch := RumBatch{
		Session:     "sess-abc",
		Release:     "v0.25.0",
		DeviceClass: "desktop",
		Connection:  "4g",
		Events: []RumEvent{
			{Kind: "web-vital", Name: "web-vital.lcp", RouteTemplate: "/w/:ws/c/:channel", Value: 1200},
			{Kind: "landmark", Name: "calendar.month-view.render", RouteTemplate: "/w/:ws/calendar", Value: 80},
			// dirty: concrete uuid in route → dropped.
			{Kind: "web-vital", Name: "web-vital.cls", RouteTemplate: "/w/019e24ef-c454-7000-8000-0000000000aa", Value: 0.1},
			// dirty: non-finite value → dropped.
			{Kind: "web-vital", Name: "web-vital.inp", RouteTemplate: "/login", Value: inf()},
			// dirty: unknown kind → dropped.
			{Kind: "custom", Name: "x.y", RouteTemplate: "/login", Value: 1},
		},
	}

	inserted, dropped, err := svc.IngestRum(context.Background(), batch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inserted != 2 || dropped != 3 {
		t.Fatalf("expected inserted=2 dropped=3, got inserted=%d dropped=%d", inserted, dropped)
	}
	if len(repo.rumRows) != 2 {
		t.Fatalf("expected 2 persisted rows, got %d", len(repo.rumRows))
	}
	wantBucket := clock.Unix() / 10
	for _, row := range repo.rumRows {
		if row.TSBucket != wantBucket {
			t.Errorf("expected bucket %d, got %d", wantBucket, row.TSBucket)
		}
		if !row.TS.Equal(clock) {
			t.Errorf("expected ts %v, got %v", clock, row.TS)
		}
		if row.Session != "sess-abc" || row.Release != "v0.25.0" || row.DeviceClass != "desktop" || row.Connection != "4g" {
			t.Errorf("batch metadata not propagated to row: %+v", row)
		}
	}
}

func TestIngestRumAllDirtySkipsRepo(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	batch := RumBatch{
		Session: "s",
		Events: []RumEvent{
			{Kind: "web-vital", Name: "web-vital.lcp", RouteTemplate: "/c/123456", Value: 1},
		},
	}
	inserted, dropped, err := svc.IngestRum(context.Background(), batch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inserted != 0 || dropped != 1 {
		t.Fatalf("expected inserted=0 dropped=1, got inserted=%d dropped=%d", inserted, dropped)
	}
	if len(repo.rumRows) != 0 {
		t.Fatalf("expected no repo call, got %d rows", len(repo.rumRows))
	}
}

func TestIngestLabUpsertsRunThenMetrics(t *testing.T) {
	repo := newFakeRepo()
	svc := NewService(repo)
	runs := []LabRun{
		{
			Commit: "abc", Branch: "develop", Profile: "desktop", Environment: "mocked", Scenario: "common.load",
			Metrics: []LabMetric{
				{Key: "web-vital.lcp", RouteTemplate: "/login", Case: "default", Value: 900, Unit: "ms"},
				{Key: "bundle.route-js", RouteTemplate: "/login", Case: "default", Value: 120, Unit: "kb"},
			},
		},
	}
	if err := svc.IngestLab(context.Background(), runs); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.runs) != 1 {
		t.Fatalf("expected 1 upserted run, got %d", len(repo.runs))
	}
	if got := repo.metricsFor[repo.runID]; len(got) != 2 {
		t.Fatalf("expected 2 metrics under run id, got %d", len(got))
	}
}

func inf() float64 {
	var zero float64
	return 1 / zero
}
