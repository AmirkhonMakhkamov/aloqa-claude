package postgres

import (
	"testing"

	"github.com/google/uuid"
)

// preview_url must point at the /content endpoint: the bare /api/v1/files/{id}
// route returns JSON metadata, which breaks <img>/<video> consumers.
func TestFileURLPointsAtContentEndpoint(t *testing.T) {
	id := uuid.MustParse("019eba07-9ebf-71ff-885d-8ab582f13b2d")

	got := fileURL(id, "inline")
	want := "/api/v1/files/" + id.String() + "/content?disposition=inline"
	if got != want {
		t.Fatalf("fileURL with disposition = %q, want %q", got, want)
	}

	got = fileURL(id, "")
	want = "/api/v1/files/" + id.String() + "/content"
	if got != want {
		t.Fatalf("fileURL without disposition = %q, want %q", got, want)
	}
}
