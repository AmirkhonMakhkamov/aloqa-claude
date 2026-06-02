package entity

import (
	"hash/fnv"
	"strings"
	"unicode"
)

var AvatarColorPalette = [...]string{
	"#2D7D6A",
	"#7C3AED",
	"#D97706",
	"#2454D8",
	"#DB2777",
	"#059669",
	"#9333EA",
	"#0EA5E9",
	"#DC2626",
	"#16A34A",
	"#CA8A04",
	"#C026D3",
	"#0891B2",
	"#4F46E5",
	"#BE123C",
	"#047857",
	"#B45309",
	"#4338CA",
	"#0F766E",
	"#A21CAF",
	"#1D4ED8",
	"#B91C1C",
	"#15803D",
	"#854D0E",
	"#6D28D9",
	"#0369A1",
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
