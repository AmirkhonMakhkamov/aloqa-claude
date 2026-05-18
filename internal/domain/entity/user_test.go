package entity

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUserDeactivatedAtJSONOmitEmpty(t *testing.T) {
	user := User{
		ID:          uuid.New(),
		Email:       "user@example.com",
		DisplayName: "User",
		Status:      UserStatusActive,
		Locale:      "en",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	data, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	if string(data) == "" {
		t.Fatalf("Marshal returned empty JSON")
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if _, ok := raw["deactivated_at"]; ok {
		t.Fatalf("deactivated_at present for nil value: %s", data)
	}
}

func TestUserDeactivatedAtJSONPresentWhenSet(t *testing.T) {
	deactivatedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	user := User{
		ID:            uuid.New(),
		Email:         "user@example.com",
		DisplayName:   "User",
		Status:        UserStatusDeactivated,
		DeactivatedAt: &deactivatedAt,
		Locale:        "en",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	data, err := json.Marshal(user)
	if err != nil {
		t.Fatalf("Marshal returned error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if raw["deactivated_at"] == nil {
		t.Fatalf("deactivated_at missing for set value: %s", data)
	}
}
