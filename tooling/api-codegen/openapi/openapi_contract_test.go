package openapi_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIDoc struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]map[string]any `yaml:"schemas"`
	} `yaml:"components"`
}

func loadSpec(t *testing.T) openAPIDoc {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to locate test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "aloqa-v1.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI spec: %v", err)
	}
	var doc openAPIDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse OpenAPI spec: %v", err)
	}
	return doc
}

func requireOperation(t *testing.T, doc openAPIDoc, method, path string) {
	t.Helper()
	pathItem, ok := doc.Paths[path]
	if !ok {
		t.Fatalf("missing OpenAPI path %s", path)
	}
	if _, ok := pathItem[method]; !ok {
		t.Fatalf("missing OpenAPI operation %s %s", method, path)
	}
}

func TestOpenAPICoversFrontendContractRoutes(t *testing.T) {
	doc := loadSpec(t)

	required := []struct {
		method string
		path   string
	}{
		{"get", "/api/v1/users/me/files"},
		{"get", "/api/v1/users/me/storage"},
		{"post", "/api/v1/files/upload"},
		{"get", "/api/v1/files/{fileID}/content"},
		{"delete", "/api/v1/files/{fileID}"},
		{"post", "/api/v1/files/{fileID}/shares"},
		{"post", "/api/v1/workspaces/{workspaceID}/channels/{channelID}/dm-request/accept"},
		{"post", "/api/v1/workspaces/{workspaceID}/channels/{channelID}/dm-request/block"},
		{"post", "/api/v1/personal/channels/{channelID}/dm-request/accept"},
		{"post", "/api/v1/personal/channels/{channelID}/dm-request/block"},
		{"get", "/api/v1/personal/channels/{channelID}/messages/{messageID}/thread"},
		{"get", "/api/v1/workspaces/{workspaceID}/channels/{channelID}/mentions"},
		{"get", "/api/v1/workspaces/{workspaceID}/calls/{callID}/recordings/{recordingID}/artifacts/{artifactID}/download"},
		{"put", "/api/v1/workspaces/{workspaceID}/admin/media/calls/{callID}/quality-policy"},
	}
	for _, item := range required {
		requireOperation(t, doc, item.method, item.path)
	}
}

func TestOpenAPIDoesNotExposeRemovedCustomSFUSignaling(t *testing.T) {
	doc := loadSpec(t)

	removedPaths := []string{
		"/api/v1/workspaces/{workspaceID}/calls/{callID}/media-session/token",
		"/api/v1/workspaces/{workspaceID}/calls/{callID}/media-session/offer",
		"/api/v1/workspaces/{workspaceID}/calls/{callID}/media-session/ice-candidate",
		"/api/auth/refresh",
		"/api/auth/logout",
	}
	for _, path := range removedPaths {
		if _, ok := doc.Paths[path]; ok {
			t.Fatalf("OpenAPI spec must not expose removed or frontend-only path %s", path)
		}
	}
}

func TestOpenAPIDMRequestStatusContract(t *testing.T) {
	doc := loadSpec(t)

	channelProps := requiredProperties(t, doc, "Channel")
	if _, ok := channelProps["dm_request_status"]; !ok {
		t.Fatal("Channel schema is missing dm_request_status")
	}

	memberProps := requiredProperties(t, doc, "ChannelMember")
	if _, ok := memberProps["dm_request_status"]; !ok {
		t.Fatal("ChannelMember schema is missing dm_request_status")
	}
}

func TestOpenAPIFileMessageContract(t *testing.T) {
	doc := loadSpec(t)

	messageProps := requiredProperties(t, doc, "Message")
	if _, ok := messageProps["file_ids"]; !ok {
		t.Fatal("Message schema is missing file_ids")
	}
	if _, ok := messageProps["files"]; !ok {
		t.Fatal("Message schema is missing files")
	}
	sendProps := requiredProperties(t, doc, "SendMessageRequest")
	if _, ok := sendProps["file_ids"]; !ok {
		t.Fatal("SendMessageRequest schema is missing file_ids")
	}

	fileTypeFacetProps := requiredProperties(t, doc, "FileTypeFacet")
	typeSchema, ok := fileTypeFacetProps["type"].(map[string]any)
	if !ok {
		t.Fatal("FileTypeFacet.type schema has unexpected shape")
	}
	values, ok := typeSchema["enum"].([]any)
	if !ok {
		t.Fatal("FileTypeFacet.type enum has unexpected shape")
	}
	seen := map[string]bool{}
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			t.Fatalf("FileTypeFacet.type enum contains non-string value %v", value)
		}
		seen[text] = true
	}
	if seen["text"] {
		t.Fatal("FileTypeFacet.type must not expose text; frontend FileCategory rejects it")
	}
	for _, value := range []string{"image", "document", "archive", "video", "audio", "code"} {
		if !seen[value] {
			t.Fatalf("FileTypeFacet.type enum is missing %s", value)
		}
	}
}

func requiredProperties(t *testing.T, doc openAPIDoc, schemaName string) map[string]any {
	t.Helper()
	schema, ok := doc.Components.Schemas[schemaName]
	if !ok {
		t.Fatalf("missing schema %s", schemaName)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema %s has no object properties", schemaName)
	}
	return props
}
