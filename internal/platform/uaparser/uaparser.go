package uaparser

import (
	"fmt"

	"github.com/mileusna/useragent"
)

// Parse extracts user-facing device, browser, and OS labels from a UA string.
// Returns empty strings on unrecognised input; never panics.
func Parse(raw string) (device, browser, os string) {
	if raw == "" {
		return "", "", ""
	}
	ua := useragent.Parse(raw)
	return labelDevice(ua), labelBrowser(ua), labelOS(ua)
}

func labelDevice(ua useragent.UserAgent) string {
	switch {
	case ua.Tablet:
		return "Tablet"
	case ua.Mobile:
		return "Mobile"
	case ua.Desktop:
		return "Desktop"
	default:
		return ""
	}
}

func labelBrowser(ua useragent.UserAgent) string {
	switch ua.Name {
	case useragent.Chrome, useragent.Firefox, useragent.Edge, useragent.Opera, useragent.OperaMini, useragent.OperaTouch, useragent.SamsungBrowser:
		return labelWithMajorVersion(ua.Name, ua.VersionNo.Major)
	case useragent.Safari:
		if ua.Mobile {
			return useragent.Safari
		}
		return labelWithMajorVersion(useragent.Safari, ua.VersionNo.Major)
	default:
		return ""
	}
}

func labelOS(ua useragent.UserAgent) string {
	switch ua.OS {
	case useragent.MacOS:
		return labelWithVersion(useragent.MacOS, ua.OSVersionNo.Major, ua.OSVersionNo.Minor)
	case useragent.IOS:
		return labelWithMajorVersion(useragent.IOS, ua.OSVersionNo.Major)
	case useragent.Android:
		return labelWithMajorVersion(useragent.Android, ua.OSVersionNo.Major)
	case useragent.Windows:
		return labelWindows(ua.OSVersionNo.Major, ua.OSVersionNo.Minor)
	case useragent.Linux:
		return useragent.Linux
	default:
		return ""
	}
}

func labelWithMajorVersion(name string, major int) string {
	if major <= 0 {
		return name
	}
	return fmt.Sprintf("%s %d", name, major)
}

func labelWithVersion(name string, major, minor int) string {
	if major <= 0 {
		return name
	}
	if minor <= 0 {
		return fmt.Sprintf("%s %d", name, major)
	}
	return fmt.Sprintf("%s %d.%d", name, major, minor)
}

func labelWindows(major, minor int) string {
	switch {
	case major >= 11:
		return fmt.Sprintf("Windows %d", major)
	case major == 10:
		return "Windows 10"
	case major == 6 && minor == 3:
		return "Windows 8.1"
	case major == 6 && minor == 2:
		return "Windows 8"
	case major == 6 && minor == 1:
		return "Windows 7"
	case major > 0:
		return fmt.Sprintf("Windows %d", major)
	default:
		return useragent.Windows
	}
}
