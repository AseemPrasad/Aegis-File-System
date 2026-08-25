package objectstorage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
)

const (
	minioPresignTTL  = 15 * time.Minute
	minioBlockPrefix = "blocks/"
)

// MinIOClient implements ObjectStorageClient backed by MinIO.
type MinIOClient struct {
	client *minio.Client
	bucket string
	logger *slog.Logger
}

// MinIOConfig holds configuration for the MinIO backend.
type MinIOConfig struct {
	Endpoint  string // host:port
	AccessKey string
	SecretKey string
	Bucket    string
	UseSSL    bool
}

// NewMinIOClient builds a MinIO client and ensures the bucket exists.
func NewMinIOClient(cfg MinIOConfig, lg *slog.Logger) (*MinIOClient, error) {
	if lg == nil {
		lg = slog.Default()
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("objectstorage: bucket name is required")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("objectstorage: endpoint is required")
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("objectstorage: create minio client: %w", err)
	}

	ctx := context.Background()
	exists, err := client.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("objectstorage: check bucket: %w", err)
	}
	if !exists {
		if err := client.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("objectstorage: create bucket: %w", err)
		}
		lg.Info("minio bucket created", "bucket", cfg.Bucket)
	}

	return &MinIOClient{client: client, bucket: cfg.Bucket, logger: lg}, nil
}

func (c *MinIOClient) blockKey(blockHash string) string {
	return minioBlockPrefix + blockHash
}

func (c *MinIOClient) GenerateUploadURL(_ context.Context, tenantID, blockHash string, _ int64) (string, error) {
	key := c.blockKey(blockHash)

	policy := minio.NewPostPolicy()
	_ = policy.SetBucket(c.bucket)
	_ = policy.SetKey(key)
	_ = policy.SetExpires(time.Now().Add(minioPresignTTL))
	_ = policy.SetContentType("application/octet-stream")
	_ = policy.SetTagging(fmt.Sprintf("tenant=%s", tenantID))
	policy.SetEncryption(encrypt.NewSSE())

	_, formData, err := c.client.PresignedPostPolicy(context.Background(), policy)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUploadFailed, err)
	}

	// Build the full upload URL from form data.
	endpoint := c.client.EndpointURL().String()
	uploadURL := fmt.Sprintf("%s/%s/%s", endpoint, c.bucket, key)

	_ = formData // form data included in the response for POST uploads

	return uploadURL, nil
}

func isMinioNotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == 404
}

func (c *MinIOClient) VerifyBlock(ctx context.Context, blockHash, expectedETag string) error {
	info, err := c.client.StatObject(ctx, c.bucket, c.blockKey(blockHash), minio.StatObjectOptions{})
	if err != nil {
		if isMinioNotFound(err) {
			return fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
		}
		return err
	}

	stored := strings.Trim(info.ETag, `"`)
	expected := strings.Trim(expectedETag, `"`)
	if stored != expected {
		return fmt.Errorf("%w: stored=%s expected=%s", ErrETagMismatch, stored, expected)
	}
	return nil
}

func (c *MinIOClient) GetBlockMetadata(ctx context.Context, blockHash string) (*BlockMetadata, error) {
	info, err := c.client.StatObject(ctx, c.bucket, c.blockKey(blockHash), minio.StatObjectOptions{})
	if err != nil {
		if isMinioNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
		}
		return nil, err
	}

	tier := TierHot
	if info.StorageClass != "" {
		switch info.StorageClass {
		case "STANDARD_IA", "ONEZONE_IA":
			tier = TierWarm
		case "GLACIER", "DEEP_ARCHIVE":
			tier = TierCold
		}
	}

	return &BlockMetadata{
		BlockHash:    blockHash,
		ETag:         strings.Trim(info.ETag, `"`),
		SizeBytes:    info.Size,
		StorageTier:  tier,
		LastModified: info.LastModified,
	}, nil
}

func (c *MinIOClient) GetBlockReader(ctx context.Context, blockHash string) (io.ReadCloser, int64, error) {
	obj, err := c.client.GetObject(ctx, c.bucket, c.blockKey(blockHash), minio.GetObjectOptions{})
	if err != nil {
		if isMinioNotFound(err) {
			return nil, 0, fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
		}
		return nil, 0, err
	}
	stat, err := obj.Stat()
	if err != nil {
		return nil, 0, err
	}
	return obj, stat.Size, nil
}

func (c *MinIOClient) DeleteBlock(ctx context.Context, blockHash string) error {
	err := c.client.RemoveObject(ctx, c.bucket, c.blockKey(blockHash), minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeleteFailed, err)
	}
	return nil
}

func (c *MinIOClient) ApplyLifecycle(ctx context.Context, policy LifecyclePolicy) error {
	config := lifecycle.NewConfiguration()
	for _, r := range policy.Rules {
		config.Rules = append(config.Rules, lifecycle.Rule{
			ID:     r.ID,
			Status: "Enabled",
			Prefix: minioBlockPrefix,
			Transition: lifecycle.Transition{
				Days:         lifecycle.ExpirationDays(r.TransitionDay),
				StorageClass: string(r.TargetTier),
			},
		})
	}

	if policy.AbortIncompleteMultipartDays > 0 {
		config.Rules = append(config.Rules, lifecycle.Rule{
			ID:     "abort-incomplete-multipart",
			Status: "Enabled",
			AbortIncompleteMultipartUpload: lifecycle.AbortIncompleteMultipartUpload{
				DaysAfterInitiation: lifecycle.ExpirationDays(policy.AbortIncompleteMultipartDays),
			},
		})
	}

	err := c.client.SetBucketLifecycle(ctx, c.bucket, config)
	if err != nil {
		return fmt.Errorf("objectstorage: set lifecycle: %w", err)
	}
	c.logger.Info("lifecycle policy applied", "bucket", c.bucket, "rules", len(config.Rules))
	return nil
}
