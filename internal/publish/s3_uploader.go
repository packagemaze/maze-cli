package publish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const maxConcurrentPartTransfers = 4

type S3MultipartUploader struct {
	HTTPClient s3.HTTPClient
}

func NewS3MultipartUploader() *S3MultipartUploader {
	return &S3MultipartUploader{}
}

func (u *S3MultipartUploader) Upload(ctx context.Context, artifact PlannedArtifact, path string, options UploadOptions, progress io.Writer) (UploadResult, error) {
	if err := validateS3UploadPlan(artifact); err != nil {
		return UploadResult{}, err
	}
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(artifact.Upload.Target.Endpoint),
		Credentials: credentials.NewStaticCredentialsProvider(
			artifact.Upload.Target.Credentials.AccessKeyID,
			artifact.Upload.Target.Credentials.SecretAccessKey,
			artifact.Upload.Target.Credentials.SessionToken,
		),
		HTTPClient:   u.HTTPClient,
		Region:       firstNonEmpty(artifact.Upload.Target.Region, "auto"),
		UsePathStyle: true,
	})
	uploadID := artifact.Upload.UploadID
	if uploadID == "" {
		return UploadResult{}, fmt.Errorf("Artifact transfer plan was incomplete")
	}

	expectedPartCount := artifactPartCount(artifact)
	uploaded := make(map[int32]types.CompletedPart, expectedPartCount)
	if options.Resume {
		var listErr error
		uploaded, listErr = listUploadedParts(ctx, client, artifact, uploadID)
		if listErr != nil {
			if noSuchMultipartUpload(listErr) {
				return UploadResult{PartCount: expectedPartCount, UploadID: uploadID}, &artifactTransferCompletionUncertainError{cause: listErr}
			}
			return UploadResult{}, fmt.Errorf("inspect prior Artifact transfer progress")
		}
	}
	parts, uploadErr := uploadParts(ctx, client, artifact, path, uploadID, uploaded, progress)
	if uploadErr != nil {
		return UploadResult{}, uploadErr
	}
	_, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:   aws.String(artifact.Upload.Target.Bucket),
		Key:      aws.String(artifact.Upload.Target.ObjectKey),
		UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: parts,
		},
	})
	if err != nil {
		return UploadResult{PartCount: len(parts), UploadID: uploadID}, &artifactTransferCompletionUncertainError{cause: err}
	}
	return UploadResult{PartCount: len(parts), UploadID: uploadID}, nil
}

func noSuchMultipartUpload(err error) bool {
	var absent *types.NoSuchUpload
	if errors.As(err, &absent) {
		return true
	}
	var apiError smithy.APIError
	return errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchUpload"
}

func listUploadedParts(ctx context.Context, client *s3.Client, artifact PlannedArtifact, uploadID string) (map[int32]types.CompletedPart, error) {
	expectedPartCount := artifactPartCount(artifact)
	parts := make(map[int32]types.CompletedPart, expectedPartCount)
	seen := make(map[int32]struct{}, expectedPartCount)
	seenMarkers := make(map[string]struct{})
	var marker *string
	for {
		page, err := client.ListParts(ctx, &s3.ListPartsInput{
			Bucket:           aws.String(artifact.Upload.Target.Bucket),
			Key:              aws.String(artifact.Upload.Target.ObjectKey),
			PartNumberMarker: marker,
			UploadId:         aws.String(uploadID),
		})
		if err != nil {
			return nil, err
		}
		for _, part := range page.Parts {
			if part.PartNumber == nil || part.Size == nil || part.ETag == nil {
				return nil, fmt.Errorf("Artifact transfer progress was invalid")
			}
			partNumber := *part.PartNumber
			if partNumber < 1 || int(partNumber) > expectedPartCount || strings.TrimSpace(*part.ETag) == "" {
				return nil, fmt.Errorf("Artifact transfer progress was invalid")
			}
			if _, exists := seen[partNumber]; exists {
				return nil, fmt.Errorf("Artifact transfer progress was invalid")
			}
			seen[partNumber] = struct{}{}
			if *part.Size != artifactPartSize(artifact, int(partNumber)) {
				continue
			}
			parts[partNumber] = types.CompletedPart{
				ETag:       part.ETag,
				PartNumber: part.PartNumber,
			}
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		if page.NextPartNumberMarker == nil || strings.TrimSpace(*page.NextPartNumberMarker) == "" {
			return nil, fmt.Errorf("Artifact transfer progress was invalid")
		}
		next := *page.NextPartNumberMarker
		if _, duplicate := seenMarkers[next]; duplicate {
			return nil, fmt.Errorf("Artifact transfer progress was invalid")
		}
		seenMarkers[next] = struct{}{}
		marker = &next
	}
	return parts, nil
}

func uploadParts(ctx context.Context, client *s3.Client, artifact PlannedArtifact, path string, uploadID string, uploaded map[int32]types.CompletedPart, progress io.Writer) ([]types.CompletedPart, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	partCount := artifactPartCount(artifact)
	completed := make([]types.CompletedPart, partCount)
	missing := make([]int, 0, partCount-len(uploaded))
	for partIndex := 0; partIndex < partCount; partIndex++ {
		partNumber := int32(partIndex + 1)
		if existing, ok := uploaded[partNumber]; ok {
			completed[partIndex] = existing
			if progress != nil {
				_, _ = fmt.Fprintf(progress, "Reused transferred part %d of %s\n", partNumber, artifact.Artifact.Filename)
			}
			continue
		}
		missing = append(missing, partIndex)
	}
	if partCount == 0 {
		return nil, fmt.Errorf("upload artifact was empty")
	}
	if err := uploadMissingParts(ctx, client, artifact, file, uploadID, missing, completed, progress); err != nil {
		return nil, err
	}
	return completed, nil
}

type partTransferResult struct {
	err       error
	part      types.CompletedPart
	partIndex int
}

func uploadMissingParts(ctx context.Context, client *s3.Client, artifact PlannedArtifact, file *os.File, uploadID string, missing []int, completed []types.CompletedPart, progress io.Writer) error {
	if len(missing) == 0 {
		return nil
	}
	transferCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int, len(missing))
	results := make(chan partTransferResult, len(missing))
	for _, partIndex := range missing {
		jobs <- partIndex
	}
	close(jobs)
	workerCount := min(maxConcurrentPartTransfers, len(missing))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for partIndex := range jobs {
				if err := transferCtx.Err(); err != nil {
					results <- partTransferResult{err: err, partIndex: partIndex}
					continue
				}
				part, err := uploadOnePart(transferCtx, client, artifact, file, uploadID, partIndex)
				if err != nil {
					cancel()
				}
				results <- partTransferResult{err: err, part: part, partIndex: partIndex}
			}
		}()
	}
	workers.Wait()
	close(results)
	errorsByPart := make([]error, artifactPartCount(artifact))
	for result := range results {
		errorsByPart[result.partIndex] = result.err
		if result.err == nil {
			completed[result.partIndex] = result.part
		}
	}
	for _, err := range errorsByPart {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errorsByPart {
		if err != nil {
			return err
		}
	}
	if progress != nil {
		for _, partIndex := range missing {
			_, _ = fmt.Fprintf(progress, "Uploaded part %d of %s\n", partIndex+1, artifact.Artifact.Filename)
		}
	}
	return nil
}

func uploadOnePart(ctx context.Context, client *s3.Client, artifact PlannedArtifact, file *os.File, uploadID string, partIndex int) (types.CompletedPart, error) {
	partNumber := int32(partIndex + 1)
	readSize := artifactPartSize(artifact, partIndex+1)
	part := io.NewSectionReader(file, int64(partIndex)*artifact.Upload.PartSizeBytes, readSize)
	output, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Body:          part,
		Bucket:        aws.String(artifact.Upload.Target.Bucket),
		ContentLength: aws.Int64(readSize),
		Key:           aws.String(artifact.Upload.Target.ObjectKey),
		PartNumber:    aws.Int32(partNumber),
		UploadId:      aws.String(uploadID),
	})
	if err != nil {
		return types.CompletedPart{}, fmt.Errorf("transfer Artifact part %d: %w", partNumber, err)
	}
	if output.ETag == nil || strings.TrimSpace(*output.ETag) == "" {
		return types.CompletedPart{}, fmt.Errorf("transfer Artifact part %d returned an invalid receipt", partNumber)
	}
	return types.CompletedPart{
		ETag:       output.ETag,
		PartNumber: aws.Int32(partNumber),
	}, nil
}

func artifactPartCount(artifact PlannedArtifact) int {
	if artifact.Artifact.SizeBytes <= 0 {
		return 0
	}
	return int((artifact.Artifact.SizeBytes + artifact.Upload.PartSizeBytes - 1) / artifact.Upload.PartSizeBytes)
}

func artifactPartSize(artifact PlannedArtifact, partNumber int) int64 {
	offset := int64(partNumber-1) * artifact.Upload.PartSizeBytes
	remaining := artifact.Artifact.SizeBytes - offset
	if remaining < artifact.Upload.PartSizeBytes {
		return remaining
	}
	return artifact.Upload.PartSizeBytes
}
