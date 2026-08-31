package r2inventory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func objectType(key string) string {
	switch {
	case strings.HasPrefix(key, "assets/"):
		return "assets"
	case strings.HasPrefix(key, "project-workspaces/"):
		return "history"
	case strings.Contains(key, "project.json") || strings.HasPrefix(key, "project-json/"):
		return "projectData"
	default:
		return "other"
	}
}

func scan(ctx context.Context, endpoint, bucket, accessKey, secretKey, region string, httpClient *http.Client) map[string]any {
	parsedEndpoint, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || parsedEndpoint.Scheme == "" || parsedEndpoint.Host == "" {
		return map[string]any{"ok": false, "error": "invalid R2 endpoint"}
	}
	if bucket == "" || accessKey == "" || secretKey == "" {
		return map[string]any{"ok": false, "error": "R2 credentials are incomplete"}
	}
	if region == "" {
		region = "auto"
	}
	config := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""),
		HTTPClient:  httpClient,
	}
	client := s3.NewFromConfig(config, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(strings.TrimRight(parsedEndpoint.String(), "/"))
		options.UsePathStyle = true
	})
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	objects := make(map[string]any)
	pages := 0
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		pages++
		for _, item := range page.Contents {
			storedAt := int64(0)
			if item.LastModified != nil {
				storedAt = item.LastModified.UnixMilli()
			}
			key := aws.ToString(item.Key)
			objects[key] = map[string]any{
				"type": objectType(key), "size": aws.ToInt64(item.Size), "storedAt": storedAt,
			}
		}
	}
	return map[string]any{"ok": true, "objects": objects, "pages": pages, "syncedAt": time.Now().UnixMilli()}
}

// ScanJSON lists every object already present in R2 without downloading object
// bodies. The returned inventory is persisted by the OSL layer.
func ScanJSON(endpoint, bucket, accessKey, secretKey, region string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result := scan(ctx, endpoint, bucket, accessKey, secretKey, region, &http.Client{Timeout: 30 * time.Second})
	encoded, err := json.Marshal(result)
	if err != nil {
		return `{"ok":false,"error":"could not encode R2 inventory"}`
	}
	return string(encoded)
}
