package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadReadsDotEnvFromWorkingDirectory(t *testing.T) {
	preserveEnv(t, "DB_USER", "DB_PASSWORD", "DB_NAME", "JWT_SECRET", "SERVER_PORT")

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
	preserveEnv(t, "DB_USER", "DB_PASSWORD", "DB_NAME", "JWT_SECRET")

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
