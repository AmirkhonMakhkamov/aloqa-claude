package extension

import (
	"context"

	"github.com/google/uuid"
)

// App represents a third-party application registered in the marketplace.
type App struct {
	ID          uuid.UUID         `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Developer   string            `json:"developer"`
	IconURL     string            `json:"icon_url,omitempty"`
	WebhookURL  string            `json:"webhook_url"`
	Permissions []string          `json:"permissions"`
	SlashCommands []SlashCommand  `json:"slash_commands,omitempty"`
	Status      string            `json:"status"` // active, suspended, review
}

// SlashCommand defines a command that users can invoke in chat.
type SlashCommand struct {
	Command     string `json:"command"`     // e.g., "/poll"
	Description string `json:"description"`
	Usage       string `json:"usage"`       // e.g., "/poll <question>"
}

// AppInstallation records which apps are installed in a workspace.
type AppInstallation struct {
	ID          uuid.UUID `json:"id"`
	AppID       uuid.UUID `json:"app_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	InstalledBy uuid.UUID `json:"installed_by"`
	Config      map[string]any `json:"config,omitempty"`
}

// WebhookEvent is the payload delivered to app webhook endpoints.
type WebhookEvent struct {
	EventID     uuid.UUID `json:"event_id"`
	AppID       uuid.UUID `json:"app_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Type        string    `json:"type"` // message.created, call.started, etc.
	Payload     any       `json:"payload"`
	Timestamp   int64     `json:"timestamp"`
}

// MarketplaceService manages the app marketplace lifecycle.
type MarketplaceService interface {
	// RegisterApp registers a new third-party app.
	RegisterApp(ctx context.Context, app *App) error
	// InstallApp installs an app in a workspace.
	InstallApp(ctx context.Context, appID, workspaceID, userID uuid.UUID) (*AppInstallation, error)
	// UninstallApp removes an app from a workspace.
	UninstallApp(ctx context.Context, appID, workspaceID uuid.UUID) error
	// ListApps returns available apps.
	ListApps(ctx context.Context) ([]App, error)
	// ListInstalled returns apps installed in a workspace.
	ListInstalled(ctx context.Context, workspaceID uuid.UUID) ([]AppInstallation, error)
	// DeliverWebhook sends a webhook event to the app's endpoint.
	DeliverWebhook(ctx context.Context, appID uuid.UUID, event WebhookEvent) error
	// HandleSlashCommand routes a slash command to the appropriate app.
	HandleSlashCommand(ctx context.Context, workspaceID uuid.UUID, command, args string, userID uuid.UUID) (string, error)
}

// BotUser represents a system user that an app operates as.
type BotUser struct {
	ID          uuid.UUID `json:"id"`
	AppID       uuid.UUID `json:"app_id"`
	DisplayName string    `json:"display_name"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
}
