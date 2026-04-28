package extension

import "github.com/google/uuid"

// BrandingConfig holds per-workspace white-label customization.
type BrandingConfig struct {
	WorkspaceID    uuid.UUID         `json:"workspace_id"`
	CustomDomain   string            `json:"custom_domain,omitempty"`
	LogoURL        string            `json:"logo_url,omitempty"`
	FaviconURL     string            `json:"favicon_url,omitempty"`
	PrimaryColor   string            `json:"primary_color,omitempty"`
	SecondaryColor string            `json:"secondary_color,omitempty"`
	AccentColor    string            `json:"accent_color,omitempty"`
	FontFamily     string            `json:"font_family,omitempty"`
	AppName        string            `json:"app_name,omitempty"`
	SupportEmail   string            `json:"support_email,omitempty"`
	FeatureFlags   map[string]bool   `json:"feature_flags,omitempty"`
	CustomCSS      string            `json:"custom_css,omitempty"`
}

// FeatureFlag names for workspace-level toggles.
const (
	FeatureTelephony    = "telephony"
	FeatureAI           = "ai"
	FeatureRecording    = "recording"
	FeatureBreakout     = "breakout_rooms"
	FeatureE2EE         = "e2ee"
	FeatureGuests       = "guests"
	FeatureMarketplace  = "marketplace"
	FeatureScreenShare  = "screen_sharing"
)

// WhiteLabelService manages per-workspace branding configuration.
type WhiteLabelService interface {
	GetBranding(workspaceID uuid.UUID) (*BrandingConfig, error)
	UpdateBranding(workspaceID uuid.UUID, config *BrandingConfig) error
	IsFeatureEnabled(workspaceID uuid.UUID, feature string) bool
	ResolveDomain(domain string) (workspaceID uuid.UUID, err error)
}
