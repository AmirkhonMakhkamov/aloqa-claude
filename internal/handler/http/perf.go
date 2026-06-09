package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"aloqa/internal/pkg/cerrors"
	"aloqa/internal/service/perf"
)

// perfService is the subset of *perf.Service the handler depends on
// (consumer-defined interface so handler tests can inject a fake).
type perfService interface {
	IngestLab(ctx context.Context, runs []perf.LabRun) error
	IngestRum(ctx context.Context, batch perf.RumBatch) (inserted, dropped int, err error)
}

// PerfHandler ingests performance telemetry (epic ALK-849). Both endpoints are
// authenticated by a static service token compared in constant time — there is
// no user session. An empty configured token disables the endpoint (503), so an
// environment without perf storage provisioned never breaks.
type PerfHandler struct {
	svc      perfService
	labToken string
	rumToken string
}

// NewPerfHandler wires the perf service together with the lab + rum service tokens.
func NewPerfHandler(svc perfService, labToken, rumToken string) *PerfHandler {
	return &PerfHandler{svc: svc, labToken: labToken, rumToken: rumToken}
}

const (
	// maxLabBodyBytes caps the CI result.json (many metrics across scenarios).
	maxLabBodyBytes = 4 << 20 // 4 MB
	// maxRumBodyBytes caps one sampled field beacon batch (<=200 events).
	maxRumBodyBytes = 256 << 10 // 256 KB
	// maxLabMetricsPerRun bounds a single run's metric array (DoS guard).
	maxLabMetricsPerRun = 2000
)

type labMetricDTO struct {
	Key           string  `json:"key"`
	RouteTemplate string  `json:"routeTemplate"`
	Case          string  `json:"case"`
	Value         float64 `json:"value"`
	Unit          string  `json:"unit"`
}

type labRunDTO struct {
	Commit      string         `json:"commit"`
	Branch      string         `json:"branch"`
	Profile     string         `json:"profile"`
	Environment string         `json:"environment"`
	Scenario    string         `json:"scenario"`
	TS          string         `json:"ts"`
	CIRunURL    string         `json:"ciRunUrl"`
	Metrics     []labMetricDTO `json:"metrics"`
}

type rumEventDTO struct {
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	Value         float64 `json:"value"`
	Rating        string  `json:"rating"`
	RouteTemplate string  `json:"routeTemplate"`
}

type rumBatchDTO struct {
	Session     string        `json:"session"`
	Release     string        `json:"release"`
	DeviceClass string        `json:"deviceClass"`
	Connection  string        `json:"connection"`
	Events      []rumEventDTO `json:"events"`
}

// IngestLab persists a CI lab run (or array of runs). Idempotent upsert by the
// run identity tuple, so a CI retry rewrites the same rows.
func (h *PerfHandler) IngestLab(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r, h.labToken); err != nil {
		writeErr(w, err)
		return
	}
	body, err := readLimitedBody(w, r, maxLabBodyBytes)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos, err := decodeLabRuns(body)
	if err != nil {
		writeErr(w, err)
		return
	}
	runs := make([]perf.LabRun, 0, len(dtos))
	for i := range dtos {
		run, convErr := dtos[i].toLabRun()
		if convErr != nil {
			writeErr(w, convErr)
			return
		}
		runs = append(runs, run)
	}
	if err := h.svc.IngestLab(r.Context(), runs); err != nil {
		writeErr(w, err)
		return
	}
	writeNoContent(w)
}

// IngestRUM persists a sampled field beacon forwarded by the FE BFF. The service
// re-validates each event and drops anything still carrying a concrete id; the
// response is always 204 (fire-and-forget, no per-event feedback).
func (h *PerfHandler) IngestRUM(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r, h.rumToken); err != nil {
		writeErr(w, err)
		return
	}
	body, err := readLimitedBody(w, r, maxRumBodyBytes)
	if err != nil {
		writeErr(w, err)
		return
	}
	var dto rumBatchDTO
	if unmarshalErr := json.Unmarshal(bytes.TrimSpace(body), &dto); unmarshalErr != nil {
		writeErr(w, cerrors.InvalidInput("invalid request body"))
		return
	}
	batch, err := dto.toRumBatch()
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, _, err := h.svc.IngestRum(r.Context(), batch); err != nil {
		// Fire-and-forget contract (spec §11): a failed persist must never surface
		// to the sampled client (the BFF ignores the response). Log for
		// observability and still return 204.
		slog.ErrorContext(r.Context(), "perf rum ingest persist failed", "error", err)
	}
	writeNoContent(w)
}

// authorize enforces the static service token. An unconfigured token (empty)
// yields 503; a missing/wrong token yields 401. The comparison is constant time.
func (h *PerfHandler) authorize(r *http.Request, expected string) error {
	if expected == "" {
		return cerrors.Unavailable("perf ingest is not configured")
	}
	token := bearerToken(r)
	if token == "" {
		return cerrors.Unauthorized("invalid perf ingest token")
	}
	// Compare fixed-length SHA-256 digests so neither the comparison time nor the
	// ConstantTimeCompare equal-length precheck leaks the configured token length.
	want := sha256.Sum256([]byte(expected))
	got := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		return cerrors.Unauthorized("invalid perf ingest token")
	}
	return nil
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

func readLimitedBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			// cerrors has no 413 constructor; map an oversize body to 400.
			return nil, cerrors.InvalidInput("request body too large")
		}
		return nil, cerrors.InvalidInput("invalid request body")
	}
	return body, nil
}

// decodeLabRuns accepts a single run object or an array of runs (the result.json
// boundary). Decoding is lenient (no DisallowUnknownFields) so a future CI field
// never breaks ingest; required fields are validated in toLabRun.
func decodeLabRuns(body []byte) ([]labRunDTO, error) {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 {
		return nil, cerrors.InvalidInput("request body is required")
	}
	if trimmed[0] == '[' {
		var runs []labRunDTO
		if err := json.Unmarshal(trimmed, &runs); err != nil {
			return nil, cerrors.InvalidInput("invalid request body")
		}
		return runs, nil
	}
	var run labRunDTO
	if err := json.Unmarshal(trimmed, &run); err != nil {
		return nil, cerrors.InvalidInput("invalid request body")
	}
	return []labRunDTO{run}, nil
}

func (d labRunDTO) toLabRun() (perf.LabRun, error) {
	if strings.TrimSpace(d.Commit) == "" {
		return perf.LabRun{}, cerrors.InvalidInput("commit is required")
	}
	if strings.TrimSpace(d.Branch) == "" {
		return perf.LabRun{}, cerrors.InvalidInput("branch is required")
	}
	if d.Profile != "desktop" && d.Profile != "throttled" {
		return perf.LabRun{}, cerrors.InvalidInput("profile must be desktop or throttled")
	}
	if d.Environment != "mocked" && d.Environment != "live" {
		return perf.LabRun{}, cerrors.InvalidInput("environment must be mocked or live")
	}
	if strings.TrimSpace(d.Scenario) == "" {
		return perf.LabRun{}, cerrors.InvalidInput("scenario is required")
	}
	if len(d.Metrics) > maxLabMetricsPerRun {
		return perf.LabRun{}, cerrors.InvalidInput("too many metrics in run")
	}

	ts := time.Time{}
	if d.TS != "" {
		parsed, err := time.Parse(time.RFC3339, d.TS)
		if err != nil {
			return perf.LabRun{}, cerrors.InvalidInput("ts must be an RFC3339 timestamp")
		}
		ts = parsed.UTC()
	}

	metrics := make([]perf.LabMetric, 0, len(d.Metrics))
	for _, m := range d.Metrics {
		if strings.TrimSpace(m.Key) == "" || strings.TrimSpace(m.RouteTemplate) == "" || strings.TrimSpace(m.Unit) == "" {
			return perf.LabRun{}, cerrors.InvalidInput("metric key, routeTemplate and unit are required")
		}
		metricCase := m.Case
		if metricCase == "" {
			metricCase = "default"
		}
		metrics = append(metrics, perf.LabMetric{
			Key:           m.Key,
			RouteTemplate: m.RouteTemplate,
			Case:          metricCase,
			Value:         m.Value,
			Unit:          m.Unit,
		})
	}

	return perf.LabRun{
		Commit:      d.Commit,
		Branch:      d.Branch,
		Profile:     d.Profile,
		Environment: d.Environment,
		Scenario:    d.Scenario,
		TS:          ts,
		CIRunURL:    d.CIRunURL,
		Metrics:     metrics,
	}, nil
}

func (d rumBatchDTO) toRumBatch() (perf.RumBatch, error) {
	session := strings.TrimSpace(d.Session)
	if session == "" {
		return perf.RumBatch{}, cerrors.InvalidInput("session is required")
	}
	if len(session) > 64 {
		return perf.RumBatch{}, cerrors.InvalidInput("session is too long")
	}
	if len(d.Events) > 200 {
		return perf.RumBatch{}, cerrors.InvalidInput("too many events in batch")
	}

	events := make([]perf.RumEvent, 0, len(d.Events))
	for _, e := range d.Events {
		events = append(events, perf.RumEvent{
			Kind:          e.Kind,
			Name:          e.Name,
			RouteTemplate: e.RouteTemplate,
			Value:         e.Value,
		})
	}
	return perf.RumBatch{
		Session:     session,
		Release:     d.Release,
		DeviceClass: d.DeviceClass,
		Connection:  d.Connection,
		Events:      events,
	}, nil
}
