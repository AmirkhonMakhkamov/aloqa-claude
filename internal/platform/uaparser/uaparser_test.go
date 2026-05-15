package uaparser

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantDevice  string
		wantBrowser string
		wantOS      string
	}{
		{
			name:        "Safari 17 on macOS",
			raw:         "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15",
			wantDevice:  "Desktop",
			wantBrowser: "Safari 17",
			wantOS:      "macOS 10.15",
		},
		{
			name:        "iPhone iOS 17",
			raw:         "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			wantDevice:  "Mobile",
			wantBrowser: "Safari",
			wantOS:      "iOS 17",
		},
		{
			name:        "non-browser",
			raw:         "PostmanRuntime/7.32.3",
			wantDevice:  "",
			wantBrowser: "",
			wantOS:      "",
		},
		{
			name:        "empty",
			raw:         "",
			wantDevice:  "",
			wantBrowser: "",
			wantOS:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDevice, gotBrowser, gotOS := Parse(tt.raw)
			if gotDevice != tt.wantDevice {
				t.Fatalf("device = %q, want %q", gotDevice, tt.wantDevice)
			}
			if gotBrowser != tt.wantBrowser {
				t.Fatalf("browser = %q, want %q", gotBrowser, tt.wantBrowser)
			}
			if gotOS != tt.wantOS {
				t.Fatalf("os = %q, want %q", gotOS, tt.wantOS)
			}
		})
	}
}
