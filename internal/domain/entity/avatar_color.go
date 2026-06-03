package entity

import (
	"hash/fnv"
	"strings"
	"unicode"
)

// AvatarColorPalette holds 26 perceptually distinct colours, one per Latin
// initial (A..Z). Hues are spread across 11 perceptual bands with the muddy
// dark-yellow band deliberately under-sampled, lightness is tiered so same-band
// neighbours differ, and the index order is permuted so alphabet-adjacent
// letters land far apart on the colour wheel. Every colour keeps white-text
// contrast >= 4.5 (WCAG AA). Keep in sync with the frontend fallback
// (packages/core/src/theme/tokens.ts:avatarPalette) and migration 053.
var AvatarColorPalette = [...]string{
	"#C92642", // A
	"#298113", // B
	"#2D75BE", // C
	"#A926C9", // D
	"#CA4E2F", // E
	"#18813F", // F
	"#1D2DBF", // G
	"#CA2FB1", // H
	"#976817", // I
	"#1F847C", // J
	"#5729D6", // K
	"#B11B52", // L
	"#5E8118", // M
	"#15638A", // N
	"#9235D0", // O
	"#B11B1B", // P
	"#1F8427", // Q
	"#295DD6", // R
	"#AC1BB1", // S
	"#B05D21", // T
	"#13815C", // U
	"#4D41D2", // V
	"#C9268E", // W
	"#84771F", // X
	"#1C8292", // Y
	"#571BB1", // Z
}

func AvatarColorForDisplayName(displayName string) string {
	trimmedName := strings.TrimSpace(displayName)
	if trimmedName == "" {
		return AvatarColorPalette[0]
	}

	for _, firstRune := range trimmedName {
		upperRune := unicode.ToUpper(firstRune)
		if upperRune >= 'A' && upperRune <= 'Z' {
			return AvatarColorPalette[upperRune-'A']
		}

		hash := fnv.New32a()
		_, _ = hash.Write([]byte(trimmedName))
		return AvatarColorPalette[hash.Sum32()%uint32(len(AvatarColorPalette))]
	}

	return AvatarColorPalette[0]
}
