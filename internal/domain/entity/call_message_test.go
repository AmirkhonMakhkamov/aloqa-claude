package entity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCallMessage_MarshalsDeletedAtNull(t *testing.T) {
	m := CallMessage{
		ID:       uuid.New(),
		CallID:   uuid.New(),
		SenderID: uuid.New(),
		Body:     "hi",
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"deleted_at":null`) {
		t.Fatalf("expected deleted_at:null, got %s", b)
	}
}
