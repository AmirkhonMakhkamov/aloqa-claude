package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsDotEnvFromWorkingDirectory(t *testing.T) {
	preserveEnv(t,
		"DB_USER",
		"DB_PASSWORD",
		"DB_NAME",
		"JWT_SECRET",
		"SERVER_PORT",
		"LIVEKIT_WEBHOOK_PATH",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_KEY",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET",
	)

	dir := t.TempDir()
	secret := strings.Repeat("s", 64)
	writeDotEnv(t, dir, strings.Join([]string{
		"DB_USER=aloqa",
		"DB_PASSWORD=aloqa",
		"DB_NAME=aloqa",
		"JWT_SECRET=" + secret,
		"SERVER_PORT=9099",
	}, "\n"))
	t.Chdir(dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DB.User != "aloqa" {
		t.Fatalf("DB.User = %q, want aloqa", cfg.DB.User)
	}
	if cfg.DB.Password != "aloqa" {
		t.Fatalf("DB.Password = %q, want aloqa", cfg.DB.Password)
	}
	if cfg.DB.Name != "aloqa" {
		t.Fatalf("DB.Name = %q, want aloqa", cfg.DB.Name)
	}
	if cfg.JWT.Secret != secret {
		t.Fatalf("JWT.Secret was not loaded from .env")
	}
	if cfg.Server.Port != 9099 {
		t.Fatalf("Server.Port = %d, want 9099", cfg.Server.Port)
	}
}

func TestLoadDotEnvDoesNotOverrideExistingEnvironment(t *testing.T) {
	preserveEnv(t,
		"DB_USER",
		"DB_PASSWORD",
		"DB_NAME",
		"JWT_SECRET",
		"LIVEKIT_WEBHOOK_PATH",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_KEY",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET",
	)

	dir := t.TempDir()
	secret := strings.Repeat("s", 64)
	writeDotEnv(t, dir, strings.Join([]string{
		"DB_USER=from-file",
		"DB_PASSWORD=aloqa",
		"DB_NAME=aloqa",
		"JWT_SECRET=" + secret,
	}, "\n"))
	t.Chdir(dir)
	t.Setenv("DB_USER", "from-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DB.User != "from-env" {
		t.Fatalf("DB.User = %q, want from-env", cfg.DB.User)
	}
}

func TestNormalizeLiveKitWebhookPath(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{
			name:  "default",
			value: "",
			want:  DefaultLiveKitWebhookPath,
		},
		{
			name:  "custom path",
			value: " /ops/livekit-webhook ",
			want:  "/ops/livekit-webhook",
		},
		{
			name:  "trailing slash",
			value: "/ops/livekit-webhook/",
			want:  "/ops/livekit-webhook",
		},
		{
			name:    "relative path",
			value:   "livekit/webhook",
			wantErr: true,
		},
		{
			name:    "query string",
			value:   "/livekit/webhook?token=secret",
			wantErr: true,
		},
		{
			name:    "fragment",
			value:   "/livekit/webhook#frag",
			wantErr: true,
		},
		{
			name:    "wildcard",
			value:   "/livekit/*",
			wantErr: true,
		},
		{
			name:    "route parameter",
			value:   "/livekit/{event}",
			wantErr: true,
		},
		{
			name:    "parent segment",
			value:   "/livekit/../webhook",
			wantErr: true,
		},
		{
			name:    "empty segment",
			value:   "/livekit//webhook",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeLiveKitWebhookPath(tt.value)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NormalizeLiveKitWebhookPath() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizeLiveKitWebhookPath() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeLiveKitWebhookPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLoadNormalizesLiveKitWebhookPathAndPreviousKey(t *testing.T) {
	preserveEnv(t,
		"DB_USER",
		"DB_PASSWORD",
		"DB_NAME",
		"JWT_SECRET",
		"LIVEKIT_WEBHOOK_PATH",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_KEY",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET",
	)

	dir := t.TempDir()
	secret := strings.Repeat("s", 64)
	writeDotEnv(t, dir, strings.Join([]string{
		"DB_USER=aloqa",
		"DB_PASSWORD=aloqa",
		"DB_NAME=aloqa",
		"JWT_SECRET=" + secret,
		"LIVEKIT_WEBHOOK_PATH=/ops/livekit-webhook/",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_KEY=previous-key",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET=previous-secret",
	}, "\n"))
	t.Chdir(dir)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LiveKit.WebhookPath != "/ops/livekit-webhook" {
		t.Fatalf("LiveKit.WebhookPath = %q, want /ops/livekit-webhook", cfg.LiveKit.WebhookPath)
	}
	if cfg.LiveKit.WebhookPreviousAPIKey != "previous-key" {
		t.Fatalf("LiveKit.WebhookPreviousAPIKey = %q, want previous-key", cfg.LiveKit.WebhookPreviousAPIKey)
	}
	if cfg.LiveKit.WebhookPreviousAPISecret != "previous-secret" {
		t.Fatalf("LiveKit.WebhookPreviousAPISecret = %q, want previous-secret", cfg.LiveKit.WebhookPreviousAPISecret)
	}
}

func TestLoadRejectsPartialLiveKitWebhookPreviousKey(t *testing.T) {
	preserveEnv(t,
		"DB_USER",
		"DB_PASSWORD",
		"DB_NAME",
		"JWT_SECRET",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_KEY",
		"LIVEKIT_WEBHOOK_PREVIOUS_API_SECRET",
	)

	dir := t.TempDir()
	secret := strings.Repeat("s", 64)
	writeDotEnv(t, dir, strings.Join([]string{
		"DB_USER=aloqa",
		"DB_PASSWORD=aloqa",
		"DB_NAME=aloqa",
		"JWT_SECRET=" + secret,
		"LIVEKIT_WEBHOOK_PREVIOUS_API_KEY=previous-key",
	}, "\n"))
	t.Chdir(dir)

	_, err := Load()
	if err == nil {
		t.Fatalf("Load() error = nil, want partial previous key error")
	}
	if !strings.Contains(err.Error(), "LIVEKIT_WEBHOOK_PREVIOUS_API_KEY") {
		t.Fatalf("Load() error = %v, want previous key env names", err)
	}
}

func writeDotEnv(t *testing.T, dir string, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
}

func preserveEnv(t *testing.T, keys ...string) {
	t.Helper()

	type envValue struct {
		key   string
		value string
		isSet bool
	}

	values := make([]envValue, 0, len(keys))
	for _, key := range keys {
		value, isSet := os.LookupEnv(key)
		values = append(values, envValue{key: key, value: value, isSet: isSet})
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}

	t.Cleanup(func() {
		for _, item := range values {
			if item.isSet {
				if err := os.Setenv(item.key, item.value); err != nil {
					t.Fatalf("restore %s: %v", item.key, err)
				}
				continue
			}
			if err := os.Unsetenv(item.key); err != nil {
				t.Fatalf("restore unset %s: %v", item.key, err)
			}
		}
	})
}
