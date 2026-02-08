package utils

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// getR2Config returns AWS config for Cloudflare R2 (S3-compatible)
func getR2Config() (aws.Config, error) {
	accountID := os.Getenv("R2_ACCOUNT_ID")
	accessKey := os.Getenv("R2_ACCESS_KEY_ID")
	secretKey := os.Getenv("R2_SECRET_ACCESS_KEY")

	if accountID == "" || accessKey == "" || secretKey == "" {
		return aws.Config{}, fmt.Errorf("R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, atau R2_SECRET_ACCESS_KEY belum diatur")
	}

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion("auto"), // Required by SDK, R2 ignores this
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("gagal load R2 config: %w", err)
	}

	return cfg, nil
}

// getR2Client returns S3 client configured for Cloudflare R2
func getR2Client() (*s3.Client, error) {
	accountID := os.Getenv("R2_ACCOUNT_ID")
	if accountID == "" {
		return nil, fmt.Errorf("R2_ACCOUNT_ID belum diatur")
	}

	cfg, err := getR2Config()
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	})

	return client, nil
}

// getR2Bucket returns the R2 bucket name from env
func getR2Bucket() (string, error) {
	bucket := os.Getenv("R2_BUCKET_NAME")
	if bucket == "" {
		return "", fmt.Errorf("R2_BUCKET_NAME belum diatur")
	}
	return bucket, nil
}

// UploadToS3 uploads a file to Cloudflare R2 (S3-compatible)
func UploadToS3(objectName string, file io.Reader, fileSize int64) error {
	bucket, err := getR2Bucket()
	if err != nil {
		return err
	}

	client, err := getR2Client()
	if err != nil {
		return err
	}

	contentType := mime.TypeByExtension(path.Ext(objectName))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(objectName),
		Body:        file,
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("R2 upload gagal: %w", err)
	}

	return nil
}

// GenerateSignedURL returns a presigned GET URL for the given object
func GenerateSignedURL(objectName string, expirySeconds int64) (string, error) {
	bucket, err := getR2Bucket()
	if err != nil {
		return "", err
	}

	client, err := getR2Client()
	if err != nil {
		return "", err
	}

	presigner := s3.NewPresignClient(client)

	presigned, err := presigner.PresignGetObject(context.TODO(),
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(objectName),
		},
		func(po *s3.PresignOptions) {
			po.Expires = time.Duration(expirySeconds) * time.Second
		},
	)
	if err != nil {
		return "", fmt.Errorf("gagal presign R2 URL: %w", err)
	}

	return presigned.URL, nil
}

// UploadToS3AndPresign uploads file to R2 and returns presigned URL
func UploadToS3AndPresign(objectName string, file io.ReadSeeker, fileSize int64, expirySeconds int64) (string, error) {
	if err := UploadToS3(objectName, file, fileSize); err != nil {
		return "", err
	}
	return GenerateSignedURL(objectName, expirySeconds)
}

// DeleteFromS3 deletes a file from Cloudflare R2
func DeleteFromS3(objectName string) error {
	bucket, err := getR2Bucket()
	if err != nil {
		return err
	}

	client, err := getR2Client()
	if err != nil {
		return err
	}

	_, err = client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(objectName),
	})
	if err != nil {
		return fmt.Errorf("R2 delete gagal: %w", err)
	}

	return nil
}
