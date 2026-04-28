package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	"aloqa/internal/platform/txscope"
	searchsvc "aloqa/internal/service/search"
)

type queryable interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type TxManagerConfig struct {
	Users               *UserRepo
	Workspaces          *WorkspaceRepo
	Messages            *MessageRepo
	Channels            *ChannelRepo
	ChannelGrants       *ChannelAccessGrantRepo
	Calls               *CallRepo
	Recordings          *RecordingRepo
	Invites             *GuestInviteRepo
	GuestGrants         *GuestAccessRepo
	Roles               *WorkspaceRoleRepo
	Search              *SearchRepo
	Realtime            *RealtimeRepo
	Audit               *AuditRepo
	RealtimeMaxAttempts int
}

type TxManager struct {
	pool                *pgxpool.Pool
	users               *UserRepo
	workspaces          *WorkspaceRepo
	messages            *MessageRepo
	channels            *ChannelRepo
	channelGrants       *ChannelAccessGrantRepo
	calls               *CallRepo
	recordings          *RecordingRepo
	invites             *GuestInviteRepo
	guestGrants         *GuestAccessRepo
	roles               *WorkspaceRoleRepo
	search              *SearchRepo
	realtime            *RealtimeRepo
	audit               *AuditRepo
	realtimeMaxAttempts int
}

type txScope struct {
	users               repository.UserRepository
	workspaces          repository.WorkspaceRepository
	messages            repository.MessageRepository
	channels            repository.ChannelRepository
	channelGrants       repository.ChannelAccessGrantRepository
	calls               repository.CallRepository
	recordings          repository.RecordingRepository
	invites             repository.GuestInviteRepository
	guestGrants         repository.GuestAccessRepository
	roles               repository.WorkspaceRoleRepository
	audit               repository.AuditRepository
	search              searchsvc.Indexer
	realtime            *RealtimeRepo
	realtimeMaxAttempts int
}

func NewTxManager(pool *pgxpool.Pool, cfg TxManagerConfig) *TxManager {
	maxAttempts := cfg.RealtimeMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 8
	}
	return &TxManager{
		pool:                pool,
		users:               cfg.Users,
		workspaces:          cfg.Workspaces,
		messages:            cfg.Messages,
		channels:            cfg.Channels,
		channelGrants:       cfg.ChannelGrants,
		calls:               cfg.Calls,
		recordings:          cfg.Recordings,
		invites:             cfg.Invites,
		guestGrants:         cfg.GuestGrants,
		roles:               cfg.Roles,
		search:              cfg.Search,
		realtime:            cfg.Realtime,
		audit:               cfg.Audit,
		realtimeMaxAttempts: maxAttempts,
	}
}

func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context, scope txscope.Scope) error) error {
	if m == nil || m.pool == nil {
		return fmt.Errorf("postgres: transaction manager is not configured")
	}

	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("postgres: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	scope := &txScope{realtimeMaxAttempts: m.realtimeMaxAttempts}
	if m.realtime != nil {
		scope.realtime = m.realtime.withTx(tx)
	}
	if m.messages != nil {
		scope.messages = m.messages.withTx(tx)
	}
	if m.users != nil {
		scope.users = m.users.withTx(tx)
	}
	if m.workspaces != nil {
		scope.workspaces = m.workspaces.withTx(tx)
	}
	if m.channels != nil {
		scope.channels = m.channels.withTx(tx)
	}
	if m.channelGrants != nil {
		scope.channelGrants = m.channelGrants.withTx(tx)
	}
	if m.calls != nil {
		scope.calls = m.calls.withTx(tx)
	}
	if m.recordings != nil {
		scope.recordings = m.recordings.withTx(tx)
	}
	if m.invites != nil {
		scope.invites = m.invites.withTx(tx)
	}
	if m.guestGrants != nil {
		scope.guestGrants = m.guestGrants.withTx(tx)
	}
	if m.roles != nil {
		scope.roles = m.roles.withTx(tx)
	}
	if m.audit != nil {
		scope.audit = m.audit.withTx(tx)
	}
	if m.search != nil {
		scope.search = m.search.withTx(tx)
	}

	if err := fn(ctx, scope); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit transaction: %w", err)
	}
	committed = true
	return nil
}

func (s *txScope) Users() repository.UserRepository { return s.users }
func (s *txScope) Workspaces() repository.WorkspaceRepository {
	return s.workspaces
}
func (s *txScope) Messages() repository.MessageRepository { return s.messages }
func (s *txScope) Channels() repository.ChannelRepository { return s.channels }
func (s *txScope) ChannelGrants() repository.ChannelAccessGrantRepository {
	return s.channelGrants
}
func (s *txScope) Calls() repository.CallRepository { return s.calls }
func (s *txScope) Recordings() repository.RecordingRepository {
	return s.recordings
}
func (s *txScope) Invites() repository.GuestInviteRepository { return s.invites }
func (s *txScope) GuestGrants() repository.GuestAccessRepository {
	return s.guestGrants
}
func (s *txScope) Roles() repository.WorkspaceRoleRepository { return s.roles }
func (s *txScope) Audit() repository.AuditRepository         { return s.audit }
func (s *txScope) SearchIndexer() searchsvc.Indexer          { return s.search }
func (s *txScope) EnqueueRealtime(ctx context.Context, evt event.Event, body []byte) error {
	if s.realtime == nil {
		return nil
	}
	return s.realtime.Enqueue(ctx, evt, body, s.realtimeMaxAttempts)
}
