package entity

import "testing"

func TestAvatarColorForDisplayNameMapsLatinInitials(t *testing.T) {
	if len(AvatarColorPalette) != 26 {
		t.Fatalf("palette length = %d, want 26", len(AvatarColorPalette))
	}

	tests := map[string]string{
		"alice": AvatarColorPalette[0],
		"Bob":   AvatarColorPalette[1],
		"  Zoe": AvatarColorPalette[25],
	}
	for name, want := range tests {
		if got := AvatarColorForDisplayName(name); got != want {
			t.Fatalf("AvatarColorForDisplayName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestAvatarColorForDisplayNameHandlesNonLatinAndEmptyNames(t *testing.T) {
	if got := AvatarColorForDisplayName(""); got != AvatarColorPalette[0] {
		t.Fatalf("empty name color = %q, want %q", got, AvatarColorPalette[0])
	}
	if got := AvatarColorForDisplayName("Алишер"); got == "" {
		t.Fatal("non-latin name color is empty")
	}
}
