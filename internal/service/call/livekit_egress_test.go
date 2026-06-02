package call

import (
	"testing"

	livekitpb "github.com/livekit/protocol/livekit"
)

func TestBuildEncodedFileOutputLocal(t *testing.T) {
	key := "recordings/call-1/rec-1.mp4"
	out := buildEncodedFileOutput(EgressSettings{Enabled: true, FileRoot: "/out"}, key)

	if out.GetFileType() != livekitpb.EncodedFileType_MP4 {
		t.Fatalf("FileType = %v, want MP4", out.GetFileType())
	}
	if out.GetFilepath() != "/out/"+key {
		t.Fatalf("Filepath = %q, want %q", out.GetFilepath(), "/out/"+key)
	}
	if out.GetS3() != nil {
		t.Fatalf("local output must not set an S3 upload")
	}
}

func TestBuildEncodedFileOutputS3(t *testing.T) {
	key := "recordings/call-1/rec-1.mp4"
	out := buildEncodedFileOutput(EgressSettings{
		Enabled: true,
		S3: &EgressS3Settings{
			AccessKey: "ak", Secret: "sk", Region: "us-east-1",
			Endpoint: "http://minio:9000", Bucket: "aloqa-media", ForcePathStyle: true,
		},
	}, key)

	if out.GetFilepath() != key {
		t.Fatalf("S3 Filepath (key) = %q, want %q", out.GetFilepath(), key)
	}
	s3 := out.GetS3()
	if s3 == nil {
		t.Fatalf("S3 output must be set")
	}
	if s3.GetBucket() != "aloqa-media" || !s3.GetForcePathStyle() || s3.GetEndpoint() != "http://minio:9000" {
		t.Fatalf("S3 upload misconfigured: %+v", s3)
	}
}
