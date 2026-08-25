package objectstorage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	s3PresignTTL      = 15 * time.Minute
	s3BlockKeyPrefix  = "blocks/"
	s3LifecyclePrefix = "blocks/"
)

// S3Client implements ObjectStorageClient backed by AWS S3.
type S3Client struct {
	client     *s3.Client
	presigner  *s3.PresignClient
	bucketName string
	kmsKeyID   string
	logger     *slog.Logger
}

// S3Config holds configuration for the S3 backend.
type S3Config struct {
	Endpoint       string
	Region         string
	BucketName     string
	KMSKeyID       string
	ForcePathStyle bool
}

// NewS3Client builds an S3 client.
func NewS3Client(ctx context.Context, cfg S3Config, lg *slog.Logger) (*S3Client, error) {
	if lg == nil {
		lg = slog.Default()
	}
	if cfg.BucketName == "" {
		return nil, fmt.Errorf("objectstorage: bucket name is required")
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("objectstorage: load AWS config: %w", err)
	}

	makeOpts := func(o *s3.Options) {
		o.UsePathStyle = cfg.ForcePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}

	client := s3.NewFromConfig(awsCfg, makeOpts)

	return &S3Client{
		client:     client,
		presigner:  s3.NewPresignClient(client),
		bucketName: cfg.BucketName,
		kmsKeyID:   cfg.KMSKeyID,
		logger:     lg,
	}, nil
}

func (c *S3Client) blockKey(blockHash string) string {
	return s3BlockKeyPrefix + blockHash
}

func (c *S3Client) GenerateUploadURL(ctx context.Context, tenantID, blockHash string, _ int64) (string, error) {
	key := c.blockKey(blockHash)

	input := &s3.PutObjectInput{
		Bucket:  aws.String(c.bucketName),
		Key:     aws.String(key),
		Tagging: aws.String(fmt.Sprintf("tenant=%s", tenantID)),
	}

	if c.kmsKeyID != "" {
		input.ServerSideEncryption = types.ServerSideEncryptionAwsKms
		input.SSEKMSKeyId = aws.String(c.kmsKeyID)
	} else {
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	}

	req, err := c.presigner.PresignPutObject(ctx, input, func(opts *s3.PresignOptions) {
		opts.Expires = s3PresignTTL
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUploadFailed, err)
	}

	return req.URL, nil
}

func (c *S3Client) VerifyBlock(ctx context.Context, blockHash, expectedETag string) error {
	head, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(c.blockKey(blockHash)),
	})
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
		}
		return err
	}

	stored := strings.Trim(aws.ToString(head.ETag), `"`)
	expected := strings.Trim(expectedETag, `"`)
	if stored != expected {
		return fmt.Errorf("%w: stored=%s expected=%s", ErrETagMismatch, stored, expected)
	}
	return nil
}

func (c *S3Client) GetBlockMetadata(ctx context.Context, blockHash string) (*BlockMetadata, error) {
	head, err := c.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(c.blockKey(blockHash)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
		}
		return nil, err
	}

	var lastMod time.Time
	if head.LastModified != nil {
		lastMod = *head.LastModified
	}
	var size int64
	if head.ContentLength != nil {
		size = *head.ContentLength
	}

	tier := TierHot
	if head.StorageClass != "" {
		switch string(head.StorageClass) {
		case "STANDARD_IA", "ONEZONE_IA", "INTELLIGENT_TIERING":
			tier = TierWarm
		case "GLACIER", "DEEP_ARCHIVE", "GLACIER_IR":
			tier = TierCold
		}
	}

	return &BlockMetadata{
		BlockHash:    blockHash,
		ETag:         strings.Trim(aws.ToString(head.ETag), `"`),
		SizeBytes:    size,
		StorageTier:  tier,
		LastModified: lastMod,
	}, nil
}

func (c *S3Client) GetBlockReader(ctx context.Context, blockHash string) (io.ReadCloser, int64, error) {
	resp, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(c.blockKey(blockHash)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, 0, fmt.Errorf("%w: %s", ErrBlockNotFound, blockHash)
		}
		return nil, 0, err
	}
	var size int64
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}
	return resp.Body, size, nil
}

func (c *S3Client) DeleteBlock(ctx context.Context, blockHash string) error {
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(c.blockKey(blockHash)),
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeleteFailed, err)
	}
	return nil
}

func (c *S3Client) ApplyLifecycle(ctx context.Context, policy LifecyclePolicy) error {
	rules := make([]types.LifecycleRule, 0, len(policy.Rules)+1)
	for _, r := range policy.Rules {
		rules = append(rules, types.LifecycleRule{
			ID:     aws.String(r.ID),
			Status: types.ExpirationStatusEnabled,
			Filter: &types.LifecycleRuleFilter{
				Prefix: aws.String(s3LifecyclePrefix),
			},
			Transitions: []types.Transition{
				{
					Days:         aws.Int32(int32(r.TransitionDay)),
					StorageClass: types.TransitionStorageClass(r.TargetTier),
				},
			},
		})
	}

	if policy.AbortIncompleteMultipartDays > 0 {
		rules = append(rules, types.LifecycleRule{
			ID:     aws.String("abort-incomplete-multipart"),
			Status: types.ExpirationStatusEnabled,
			Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")},
			AbortIncompleteMultipartUpload: &types.AbortIncompleteMultipartUpload{
				DaysAfterInitiation: aws.Int32(int32(policy.AbortIncompleteMultipartDays)),
			},
		})
	}

	_, err := c.client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket:                 aws.String(c.bucketName),
		LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: rules},
	})
	if err != nil {
		return fmt.Errorf("objectstorage: put lifecycle: %w", err)
	}
	c.logger.Info("lifecycle policy applied", "bucket", c.bucketName, "rules", len(rules))
	return nil
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "NoSuchKey") || strings.Contains(msg, "NoSuchBucket") ||
		strings.Contains(msg, "NotFound") || strings.Contains(msg, "404")
}
