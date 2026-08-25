package objectstorage

import (
	"fmt"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestIsNotFound_Nil(t *testing.T) {
	if isNotFound(nil) {
		t.Error("isNotFound(nil) should return false")
	}
}

func TestIsNotFound_NotFoundType(t *testing.T) {
	err := &types.NotFound{}
	if !isNotFound(err) {
		t.Error("isNotFound(*types.NotFound) should return true")
	}
}

func TestIsNotFound_NoSuchKey(t *testing.T) {
	err := fmt.Errorf("NoSuchKey")
	if !isNotFound(err) {
		t.Error("isNotFound with NoSuchKey should return true")
	}
}

func TestIsNotFound_NoSuchBucket(t *testing.T) {
	err := fmt.Errorf("NoSuchBucket")
	if !isNotFound(err) {
		t.Error("isNotFound with NoSuchBucket should return true")
	}
}

func TestIsNotFound_404(t *testing.T) {
	err := fmt.Errorf("status 404 not found")
	if !isNotFound(err) {
		t.Error("isNotFound with 404 should return true")
	}
}

func TestIsNotFound_Unrelated(t *testing.T) {
	err := fmt.Errorf("permission denied")
	if isNotFound(err) {
		t.Error("isNotFound with unrelated error should return false")
	}
}

func TestIsNotFound_ErrorString404(t *testing.T) {
	err := fmt.Errorf("API error 404")
	if !isNotFound(err) {
		t.Error("isNotFound with API error 404 should return true")
	}
}

func TestS3ClientBlockKey(t *testing.T) {
	c := &S3Client{
		bucketName: "test-bucket",
		logger:     slog.Default(),
	}
	got := c.blockKey("abc123")
	want := "blocks/abc123"
	if got != want {
		t.Errorf("S3Client.blockKey(%q) = %q, want %q", "abc123", got, want)
	}
}

func TestMinIOClientBlockKey(t *testing.T) {
	c := &MinIOClient{
		bucket: "test-bucket",
		logger: slog.Default(),
	}
	got := c.blockKey("abc123")
	want := "blocks/abc123"
	if got != want {
		t.Errorf("MinIOClient.blockKey(%q) = %q, want %q", "abc123", got, want)
	}
}

func TestNewS3Client_EmptyBucket(t *testing.T) {
	_, err := NewS3Client(nil, S3Config{}, nil)
	if err == nil {
		t.Error("expected error for empty bucket name")
	}
}

func TestNewMinIOClient_EmptyBucket(t *testing.T) {
	_, err := NewMinIOClient(MinIOConfig{}, nil)
	if err == nil {
		t.Error("expected error for empty bucket name")
	}
}

func TestNewMinIOClient_EmptyEndpoint(t *testing.T) {
	_, err := NewMinIOClient(MinIOConfig{Bucket: "mybucket"}, nil)
	if err == nil {
		t.Error("expected error for empty endpoint")
	}
}

func TestIsMinioNotFound_Nil(t *testing.T) {
	if isMinioNotFound(nil) {
		t.Error("isMinioNotFound(nil) should return false")
	}
}
