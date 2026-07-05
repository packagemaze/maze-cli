package publish

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type R2MultipartUploader struct{}

func NewR2MultipartUploader() *R2MultipartUploader {
	return &R2MultipartUploader{}
}

func (u *R2MultipartUploader) Upload(ctx context.Context, artifact PlannedArtifact, path string, progress io.Writer) (UploadResult, error) {
	if err := validateR2UploadPlan(artifact); err != nil {
		return UploadResult{}, err
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(artifact.Upload.Target.Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			artifact.Upload.Target.Credentials.AccessKeyID,
			artifact.Upload.Target.Credentials.SecretAccessKey,
			artifact.Upload.Target.Credentials.SessionToken,
		),
		Region:       firstNonEmpty(artifact.Upload.Target.Region, "auto"),
		UsePathStyle: true,
	})
	create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket:      aws.String(artifact.Upload.Target.Bucket),
		ContentType: aws.String(artifact.Artifact.ContentType),
		Key:         aws.String(artifact.Upload.Target.ObjectKey),
	})
	if err != nil {
		return UploadResult{}, fmt.Errorf("create R2 multipart upload: %w", err)
	}
	uploadID := aws.ToString(create.UploadId)
	if uploadID == "" {
		return UploadResult{}, fmt.Errorf("R2 multipart upload did not return an upload id")
	}

	parts, uploadErr := uploadParts(ctx, client, artifact, path, uploadID, progress)
	if uploadErr != nil {
		_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(artifact.Upload.Target.Bucket),
			Key:      aws.String(artifact.Upload.Target.ObjectKey),
			UploadId: aws.String(uploadID),
		})
		return UploadResult{}, uploadErr
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(artifact.Upload.Target.Bucket),
		Key:      aws.String(artifact.Upload.Target.ObjectKey),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: parts,
		},
	})
	if err != nil {
		_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket:   aws.String(artifact.Upload.Target.Bucket),
			Key:      aws.String(artifact.Upload.Target.ObjectKey),
			UploadId: aws.String(uploadID),
		})
		return UploadResult{}, fmt.Errorf("complete R2 multipart upload: %w", err)
	}
	return UploadResult{PartCount: len(parts), R2UploadID: uploadID}, nil
}

func uploadParts(ctx context.Context, client *s3.Client, artifact PlannedArtifact, path string, uploadID string, progress io.Writer) ([]types.CompletedPart, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	partSize := int(artifact.Upload.PartSizeBytes)
	buffer := make([]byte, partSize)
	var completed []types.CompletedPart
	for partNumber := int32(1); ; partNumber++ {
		read, readErr := io.ReadFull(file, buffer)
		if readErr == io.EOF {
			break
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("read upload part: %w", readErr)
		}
		part := buffer[:read]
		output, err := client.UploadPart(ctx, &s3.UploadPartInput{
			Body:          bytes.NewReader(part),
			Bucket:        aws.String(artifact.Upload.Target.Bucket),
			ContentLength: aws.Int64(int64(read)),
			Key:           aws.String(artifact.Upload.Target.ObjectKey),
			PartNumber:    aws.Int32(partNumber),
			UploadId:      aws.String(uploadID),
		})
		if err != nil {
			return nil, fmt.Errorf("upload R2 multipart part %d: %w", partNumber, err)
		}
		completed = append(completed, types.CompletedPart{
			ETag:       output.ETag,
			PartNumber: aws.Int32(partNumber),
		})
		if progress != nil {
			_, _ = fmt.Fprintf(progress, "Uploaded part %d of %s\n", partNumber, artifact.Artifact.Filename)
		}
		if readErr == io.ErrUnexpectedEOF {
			break
		}
	}
	if len(completed) == 0 {
		return nil, fmt.Errorf("upload artifact was empty")
	}
	return completed, nil
}
