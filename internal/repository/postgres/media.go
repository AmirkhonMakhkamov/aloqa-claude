package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/entity"
	"aloqa/internal/domain/repository"
	"aloqa/internal/pkg/cerrors"
)

type MediaRepo struct {
	pool *pgxpool.Pool
}

func NewMediaRepo(pool *pgxpool.Pool) repository.MediaRepository {
	return &MediaRepo{pool: pool}
}

func (r *MediaRepo) UpsertPlacement(ctx context.Context, placement *entity.MediaRoomPlacement) error {
	if placement == nil {
		return nil
	}
	metadata, err := json.Marshal(placement.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal media placement metadata: %w", err)
	}
	query := `
		INSERT INTO media_room_placements (
			call_id, workspace_id, node_id, region, control_url, media_url,
			routing_mode, fanout_strategy, overflow_policy, screen_share_priority,
			turn_strategy, sticky, max_participants, max_presenters, max_viewers,
			metadata, assigned_at, updated_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14, $15,
			$16, $17, $18
		)
		ON CONFLICT (call_id) DO UPDATE
		SET workspace_id = EXCLUDED.workspace_id,
			node_id = EXCLUDED.node_id,
			region = EXCLUDED.region,
			control_url = EXCLUDED.control_url,
			media_url = EXCLUDED.media_url,
			routing_mode = EXCLUDED.routing_mode,
			fanout_strategy = EXCLUDED.fanout_strategy,
			overflow_policy = EXCLUDED.overflow_policy,
			screen_share_priority = EXCLUDED.screen_share_priority,
			turn_strategy = EXCLUDED.turn_strategy,
			sticky = EXCLUDED.sticky,
			max_participants = EXCLUDED.max_participants,
			max_presenters = EXCLUDED.max_presenters,
			max_viewers = EXCLUDED.max_viewers,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at`
	_, err = r.pool.Exec(ctx, query,
		placement.CallID,
		placement.WorkspaceID,
		placement.NodeID,
		placement.Region,
		placement.ControlURL,
		placement.MediaURL,
		string(placement.RoutingMode),
		string(placement.FanoutStrategy),
		string(placement.OverflowPolicy),
		string(placement.ScreenSharePriority),
		placement.TURNStrategy,
		placement.Sticky,
		placement.MaxParticipants,
		placement.MaxPresenters,
		placement.MaxViewers,
		metadata,
		placement.AssignedAt.UTC(),
		placement.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert media placement: %w", err)
	}
	return nil
}

func (r *MediaRepo) GetPlacement(ctx context.Context, callID uuid.UUID) (*entity.MediaRoomPlacement, error) {
	query := `
		SELECT call_id, workspace_id, node_id, region, control_url, media_url,
			routing_mode, fanout_strategy, overflow_policy, screen_share_priority,
			turn_strategy, sticky, max_participants, max_presenters, max_viewers,
			metadata, assigned_at, updated_at
		FROM media_room_placements
		WHERE call_id = $1`
	var placement entity.MediaRoomPlacement
	var metadata []byte
	err := r.pool.QueryRow(ctx, query, callID).Scan(
		&placement.CallID,
		&placement.WorkspaceID,
		&placement.NodeID,
		&placement.Region,
		&placement.ControlURL,
		&placement.MediaURL,
		&placement.RoutingMode,
		&placement.FanoutStrategy,
		&placement.OverflowPolicy,
		&placement.ScreenSharePriority,
		&placement.TURNStrategy,
		&placement.Sticky,
		&placement.MaxParticipants,
		&placement.MaxPresenters,
		&placement.MaxViewers,
		&metadata,
		&placement.AssignedAt,
		&placement.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("media placement not found")
		}
		return nil, fmt.Errorf("postgres: get media placement: %w", err)
	}
	if err := unmarshalJSONB(metadata, &placement.Metadata); err != nil {
		return nil, fmt.Errorf("postgres: decode media placement metadata: %w", err)
	}
	return &placement, nil
}

func (r *MediaRepo) ListPlacementsByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.MediaRoomPlacement, error) {
	query := `
		SELECT call_id, workspace_id, node_id, region, control_url, media_url,
			routing_mode, fanout_strategy, overflow_policy, screen_share_priority,
			turn_strategy, sticky, max_participants, max_presenters, max_viewers,
			metadata, assigned_at, updated_at
		FROM media_room_placements
		WHERE workspace_id = $1
		ORDER BY assigned_at DESC`
	rows, err := r.pool.Query(ctx, query, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list media placements by workspace: %w", err)
	}
	defer rows.Close()

	var placements []entity.MediaRoomPlacement
	for rows.Next() {
		var placement entity.MediaRoomPlacement
		var metadata []byte
		if err := rows.Scan(
			&placement.CallID,
			&placement.WorkspaceID,
			&placement.NodeID,
			&placement.Region,
			&placement.ControlURL,
			&placement.MediaURL,
			&placement.RoutingMode,
			&placement.FanoutStrategy,
			&placement.OverflowPolicy,
			&placement.ScreenSharePriority,
			&placement.TURNStrategy,
			&placement.Sticky,
			&placement.MaxParticipants,
			&placement.MaxPresenters,
			&placement.MaxViewers,
			&metadata,
			&placement.AssignedAt,
			&placement.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan media placement: %w", err)
		}
		if err := unmarshalJSONB(metadata, &placement.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: decode media placement metadata: %w", err)
		}
		placements = append(placements, placement)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list media placements rows: %w", err)
	}
	return placements, nil
}

func (r *MediaRepo) ReplaceRelayEdges(ctx context.Context, callID uuid.UUID, edges []entity.MediaRelayEdge) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin media relay edge tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, `DELETE FROM media_relay_edges WHERE call_id = $1`, callID); err != nil {
		return fmt.Errorf("postgres: delete media relay edges: %w", err)
	}
	for _, edge := range edges {
		metadata, err := json.Marshal(edge.Metadata)
		if err != nil {
			return fmt.Errorf("postgres: marshal media relay edge metadata: %w", err)
		}
		query := `
			INSERT INTO media_relay_edges (
				call_id, workspace_id, source_node_id, target_node_id, target_region,
				control_url, media_url, fanout_strategy, role_scope, status, sticky,
				max_participants, priority, metadata, assigned_at, updated_at
			)
			VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10, $11,
				$12, $13, $14, $15, $16
			)`
		if _, err := tx.Exec(ctx, query,
			edge.CallID,
			edge.WorkspaceID,
			edge.SourceNodeID,
			edge.TargetNodeID,
			edge.TargetRegion,
			edge.ControlURL,
			edge.MediaURL,
			string(edge.FanoutStrategy),
			string(edge.RoleScope),
			string(edge.Status),
			edge.Sticky,
			edge.MaxParticipants,
			edge.Priority,
			metadata,
			edge.AssignedAt.UTC(),
			edge.UpdatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("postgres: insert media relay edge: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit media relay edge tx: %w", err)
	}
	return nil
}

func (r *MediaRepo) ListRelayEdgesByCall(ctx context.Context, callID uuid.UUID) ([]entity.MediaRelayEdge, error) {
	return r.listRelayEdges(ctx, `SELECT call_id, workspace_id, source_node_id, target_node_id, target_region,
			control_url, media_url, fanout_strategy, role_scope, status, sticky,
			max_participants, priority, metadata, assigned_at, updated_at
		FROM media_relay_edges
		WHERE call_id = $1
		ORDER BY priority ASC, target_region ASC, target_node_id ASC`, callID)
}

func (r *MediaRepo) ListRelayEdgesByWorkspace(ctx context.Context, workspaceID uuid.UUID) ([]entity.MediaRelayEdge, error) {
	return r.listRelayEdges(ctx, `SELECT call_id, workspace_id, source_node_id, target_node_id, target_region,
			control_url, media_url, fanout_strategy, role_scope, status, sticky,
			max_participants, priority, metadata, assigned_at, updated_at
		FROM media_relay_edges
		WHERE workspace_id = $1
		ORDER BY updated_at DESC, priority ASC`, workspaceID)
}

func (r *MediaRepo) listRelayEdges(ctx context.Context, query string, arg any) ([]entity.MediaRelayEdge, error) {
	rows, err := r.pool.Query(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("postgres: list media relay edges: %w", err)
	}
	defer rows.Close()

	var edges []entity.MediaRelayEdge
	for rows.Next() {
		var edge entity.MediaRelayEdge
		var metadata []byte
		if err := rows.Scan(
			&edge.CallID,
			&edge.WorkspaceID,
			&edge.SourceNodeID,
			&edge.TargetNodeID,
			&edge.TargetRegion,
			&edge.ControlURL,
			&edge.MediaURL,
			&edge.FanoutStrategy,
			&edge.RoleScope,
			&edge.Status,
			&edge.Sticky,
			&edge.MaxParticipants,
			&edge.Priority,
			&metadata,
			&edge.AssignedAt,
			&edge.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan media relay edge: %w", err)
		}
		if err := unmarshalJSONB(metadata, &edge.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: decode media relay edge metadata: %w", err)
		}
		edges = append(edges, edge)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list media relay edges rows: %w", err)
	}
	return edges, nil
}

func (r *MediaRepo) AppendQoSSamples(ctx context.Context, samples []entity.MediaQoSSample) error {
	for _, sample := range samples {
		metadata, err := json.Marshal(sample.Metadata)
		if err != nil {
			return fmt.Errorf("postgres: marshal media qos metadata: %w", err)
		}
		query := `
			INSERT INTO media_qos_samples (
				id, workspace_id, call_id, user_id, node_id, region, stream_id, source,
				participant_role, media_kind, packet_loss_pct, jitter_ms, round_trip_time_ms,
				available_outgoing_bitrate_kbps, available_incoming_bitrate_kbps,
				bytes_sent, bytes_received, metadata, sampled_at
			)
			VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				$9, $10, $11, $12, $13,
				$14, $15, $16, $17, $18, $19
			)`
		if _, err := r.pool.Exec(ctx, query,
			sample.ID,
			sample.WorkspaceID,
			sample.CallID,
			sample.UserID,
			sample.NodeID,
			sample.Region,
			sample.StreamID,
			string(sample.Source),
			sample.ParticipantRole,
			sample.MediaKind,
			sample.PacketLossPct,
			sample.JitterMs,
			sample.RoundTripTimeMs,
			sample.AvailableOutgoingBitrateKbps,
			sample.AvailableIncomingBitrateKbps,
			sample.BytesSent,
			sample.BytesReceived,
			metadata,
			sample.SampledAt.UTC(),
		); err != nil {
			return fmt.Errorf("postgres: append media qos sample: %w", err)
		}
	}
	return nil
}

func (r *MediaRepo) ListQoSSamples(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQoSSample, error) {
	if limit <= 0 {
		limit = 200
	}
	query := `
		SELECT id, workspace_id, call_id, user_id, node_id, region, stream_id, source,
			participant_role, media_kind, packet_loss_pct, jitter_ms, round_trip_time_ms,
			available_outgoing_bitrate_kbps, available_incoming_bitrate_kbps,
			bytes_sent, bytes_received, metadata, sampled_at
		FROM media_qos_samples
		WHERE workspace_id = $1 AND call_id = $2
		ORDER BY sampled_at DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, query, workspaceID, callID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list media qos samples: %w", err)
	}
	defer rows.Close()

	var samples []entity.MediaQoSSample
	for rows.Next() {
		var sample entity.MediaQoSSample
		var metadata []byte
		if err := rows.Scan(
			&sample.ID,
			&sample.WorkspaceID,
			&sample.CallID,
			&sample.UserID,
			&sample.NodeID,
			&sample.Region,
			&sample.StreamID,
			&sample.Source,
			&sample.ParticipantRole,
			&sample.MediaKind,
			&sample.PacketLossPct,
			&sample.JitterMs,
			&sample.RoundTripTimeMs,
			&sample.AvailableOutgoingBitrateKbps,
			&sample.AvailableIncomingBitrateKbps,
			&sample.BytesSent,
			&sample.BytesReceived,
			&metadata,
			&sample.SampledAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan media qos sample: %w", err)
		}
		if err := unmarshalJSONB(metadata, &sample.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: decode media qos metadata: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list media qos samples rows: %w", err)
	}
	return samples, nil
}

func (r *MediaRepo) SummarizeQoS(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQoSSummary, error) {
	query := `
		SELECT
			COUNT(*),
			COALESCE(MAX(sampled_at), NOW()),
			COALESCE(AVG(packet_loss_pct), 0),
			COALESCE(MAX(packet_loss_pct), 0),
			COALESCE(AVG(jitter_ms), 0),
			COALESCE(MAX(jitter_ms), 0),
			COALESCE(AVG(round_trip_time_ms), 0),
			COALESCE(MAX(round_trip_time_ms), 0)
		FROM media_qos_samples
		WHERE workspace_id = $1 AND call_id = $2`
	summary := &entity.MediaQoSSummary{
		WorkspaceID: workspaceID,
		CallID:      callID,
	}
	err := r.pool.QueryRow(ctx, query, workspaceID, callID).Scan(
		&summary.SampleCount,
		&summary.LastSampledAt,
		&summary.AvgPacketLossPct,
		&summary.MaxPacketLossPct,
		&summary.AvgJitterMs,
		&summary.MaxJitterMs,
		&summary.AvgRoundTripTimeMs,
		&summary.MaxRoundTripTimeMs,
	)
	if err != nil {
		return nil, fmt.Errorf("postgres: summarize media qos: %w", err)
	}
	return summary, nil
}

func (r *MediaRepo) UpsertQualityPolicy(ctx context.Context, policy *entity.MediaQualityPolicy) error {
	if policy == nil {
		return nil
	}
	query := `
		INSERT INTO media_quality_policies (
			call_id, workspace_id, mode, alert_packet_loss_pct, alert_jitter_ms,
			alert_round_trip_time_ms, correlation_tolerance_pct, correlation_tolerance_ms,
			server_driven_enabled, server_driven_min_interval_ms,
			meeting_wide_downgrade, alerting_enabled, updated_by, updated_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		ON CONFLICT (call_id) DO UPDATE
		SET workspace_id = EXCLUDED.workspace_id,
			mode = EXCLUDED.mode,
			alert_packet_loss_pct = EXCLUDED.alert_packet_loss_pct,
			alert_jitter_ms = EXCLUDED.alert_jitter_ms,
			alert_round_trip_time_ms = EXCLUDED.alert_round_trip_time_ms,
			correlation_tolerance_pct = EXCLUDED.correlation_tolerance_pct,
			correlation_tolerance_ms = EXCLUDED.correlation_tolerance_ms,
			server_driven_enabled = EXCLUDED.server_driven_enabled,
			server_driven_min_interval_ms = EXCLUDED.server_driven_min_interval_ms,
			meeting_wide_downgrade = EXCLUDED.meeting_wide_downgrade,
			alerting_enabled = EXCLUDED.alerting_enabled,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at`
	_, err := r.pool.Exec(ctx, query,
		policy.CallID,
		policy.WorkspaceID,
		string(policy.Mode),
		policy.AlertPacketLossPct,
		policy.AlertJitterMs,
		policy.AlertRoundTripTimeMs,
		policy.CorrelationTolerancePct,
		policy.CorrelationToleranceMs,
		policy.ServerDrivenEnabled,
		policy.ServerDrivenMinInterval,
		policy.MeetingWideDowngrade,
		policy.AlertingEnabled,
		policy.UpdatedBy,
		policy.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: upsert media quality policy: %w", err)
	}
	return nil
}

func (r *MediaRepo) GetQualityPolicy(ctx context.Context, workspaceID, callID uuid.UUID) (*entity.MediaQualityPolicy, error) {
	query := `
		SELECT workspace_id, call_id, mode, alert_packet_loss_pct, alert_jitter_ms,
			alert_round_trip_time_ms, correlation_tolerance_pct, correlation_tolerance_ms,
			server_driven_enabled, server_driven_min_interval_ms,
			meeting_wide_downgrade, alerting_enabled, updated_by, updated_at
		FROM media_quality_policies
		WHERE workspace_id = $1 AND call_id = $2`
	var policy entity.MediaQualityPolicy
	err := r.pool.QueryRow(ctx, query, workspaceID, callID).Scan(
		&policy.WorkspaceID,
		&policy.CallID,
		&policy.Mode,
		&policy.AlertPacketLossPct,
		&policy.AlertJitterMs,
		&policy.AlertRoundTripTimeMs,
		&policy.CorrelationTolerancePct,
		&policy.CorrelationToleranceMs,
		&policy.ServerDrivenEnabled,
		&policy.ServerDrivenMinInterval,
		&policy.MeetingWideDowngrade,
		&policy.AlertingEnabled,
		&policy.UpdatedBy,
		&policy.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, cerrors.NotFound("media quality policy not found")
		}
		return nil, fmt.Errorf("postgres: get media quality policy: %w", err)
	}
	return &policy, nil
}

func (r *MediaRepo) UpsertQualityAlert(ctx context.Context, alert *entity.MediaQualityAlert) error {
	if alert == nil {
		return nil
	}
	metadata, err := json.Marshal(alert.Metadata)
	if err != nil {
		return fmt.Errorf("postgres: marshal media quality alert metadata: %w", err)
	}
	updateQuery := `
		UPDATE media_quality_alerts
		SET severity = $4,
			status = $5,
			message = $6,
			metadata = $7,
			updated_at = $8,
			resolved_at = CASE WHEN $5 = 'resolved' THEN $8 ELSE NULL::timestamptz END
		WHERE workspace_id = $1 AND call_id = $2 AND kind = $3 AND status = 'active'`
	tag, err := r.pool.Exec(ctx, updateQuery,
		alert.WorkspaceID,
		alert.CallID,
		alert.Kind,
		string(alert.Severity),
		string(alert.Status),
		alert.Message,
		metadata,
		alert.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("postgres: update media quality alert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		insertQuery := `
			INSERT INTO media_quality_alerts (
				id, workspace_id, call_id, kind, severity, status, message, metadata, created_at, updated_at, resolved_at
			)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL)`
		if _, err := r.pool.Exec(ctx, insertQuery,
			alert.ID,
			alert.WorkspaceID,
			alert.CallID,
			alert.Kind,
			string(alert.Severity),
			string(alert.Status),
			alert.Message,
			metadata,
			alert.CreatedAt.UTC(),
			alert.UpdatedAt.UTC(),
		); err != nil {
			return fmt.Errorf("postgres: create media quality alert: %w", err)
		}
	}
	return nil
}

func (r *MediaRepo) ResolveQualityAlert(ctx context.Context, workspaceID, callID uuid.UUID, kind string) error {
	now := time.Now().UTC()
	query := `
		UPDATE media_quality_alerts
		SET status = 'resolved', updated_at = $4, resolved_at = $4
		WHERE workspace_id = $1 AND call_id = $2 AND kind = $3 AND status = 'active'`
	if _, err := r.pool.Exec(ctx, query, workspaceID, callID, kind, now); err != nil {
		return fmt.Errorf("postgres: resolve media quality alert: %w", err)
	}
	return nil
}

func (r *MediaRepo) ListQualityAlerts(ctx context.Context, workspaceID, callID uuid.UUID, limit int) ([]entity.MediaQualityAlert, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, workspace_id, call_id, kind, severity, status, message, metadata, created_at, updated_at, resolved_at
		FROM media_quality_alerts
		WHERE workspace_id = $1 AND call_id = $2
		ORDER BY updated_at DESC
		LIMIT $3`
	rows, err := r.pool.Query(ctx, query, workspaceID, callID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres: list media quality alerts: %w", err)
	}
	defer rows.Close()

	var alerts []entity.MediaQualityAlert
	for rows.Next() {
		var alert entity.MediaQualityAlert
		var metadata []byte
		if err := rows.Scan(
			&alert.ID,
			&alert.WorkspaceID,
			&alert.CallID,
			&alert.Kind,
			&alert.Severity,
			&alert.Status,
			&alert.Message,
			&metadata,
			&alert.CreatedAt,
			&alert.UpdatedAt,
			&alert.ResolvedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan media quality alert: %w", err)
		}
		if err := unmarshalJSONB(metadata, &alert.Metadata); err != nil {
			return nil, fmt.Errorf("postgres: decode media quality alert metadata: %w", err)
		}
		alerts = append(alerts, alert)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list media quality alerts rows: %w", err)
	}
	return alerts, nil
}

func unmarshalJSONB(raw []byte, target any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}
