package txscope

import (
	"context"

	"aloqa/internal/domain/event"
	"aloqa/internal/domain/repository"
	searchsvc "aloqa/internal/service/search"
)

type Scope interface {
	Users() repository.UserRepository
	Workspaces() repository.WorkspaceRepository
	Messages() repository.MessageRepository
	Channels() repository.ChannelRepository
	ChannelGrants() repository.ChannelAccessGrantRepository
	Calls() repository.CallRepository
	Recordings() repository.RecordingRepository
	Invites() repository.GuestInviteRepository
	GuestGrants() repository.GuestAccessRepository
	Roles() repository.WorkspaceRoleRepository
	Audit() repository.AuditRepository
	SearchIndexer() searchsvc.Indexer
	EnqueueRealtime(ctx context.Context, evt event.Event, body []byte) error
}

type Manager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context, scope Scope) error) error
}
