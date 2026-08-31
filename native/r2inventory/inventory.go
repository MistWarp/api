package r2inventory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type listedObject struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	Size         int64  `xml:"Size"`
}

type listResult struct {
	Contents              []listedObject `xml:"Contents"`
	IsTruncated           bool           `xml:"IsTruncated"`
	NextContinuationToken string         `xml:"NextContinuationToken"`
}

type client struct {
	endpoint  *url.URL
	bucket    string
	accessKey string
	secretKey string
	region    string
	http      *http.Client
	now       func() time.Time
}

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

func hmacSHA256(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func signingKey(secret, date, region string) []byte {
	dateKey := hmacSHA256([]byte("AWS4"+secret), date)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, "s3")
	return hmacSHA256(serviceKey, "aws4_request")
}

func (c *client) listPage(ctx context.Context, continuation string) (listResult, error) {
	requestURL := *c.endpoint
	requestURL.Path = path.Join(strings.TrimSuffix(requestURL.Path, "/"), c.bucket)
	query := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
	if continuation != "" {
		query.Set("continuation-token", continuation)
	}
	requestURL.RawQuery = query.Encode()

	now := c.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	canonicalURI := requestURL.EscapedPath()
	canonicalQuery := requestURL.Query().Encode()
	canonicalHeaders := "host:" + requestURL.Host + "\n" +
		"x-amz-content-sha256:" + emptyPayloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := "GET\n" + canonicalURI + "\n" + canonicalQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + emptyPayloadHash
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	scope := shortDate + "/" + c.region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(requestHash[:])
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.secretKey, shortDate, c.region), stringToSign))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return listResult{}, err
	}
	request.Header.Set("x-amz-date", amzDate)
	request.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	response, err := c.http.Do(request)
	if err != nil {
		return listResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return listResult{}, fmt.Errorf("R2 list returned %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result listResult
	if err := xml.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return listResult{}, fmt.Errorf("invalid R2 inventory response: %w", err)
	}
	return result, nil
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
	c := client{
		endpoint: parsedEndpoint, bucket: bucket, accessKey: accessKey, secretKey: secretKey,
		region: region, http: httpClient, now: time.Now,
	}
	objects := make(map[string]any)
	continuation := ""
	pages := 0
	for {
		page, err := c.listPage(ctx, continuation)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error()}
		}
		pages++
		for _, item := range page.Contents {
			storedAt := int64(0)
			if parsed, parseErr := time.Parse(time.RFC3339, item.LastModified); parseErr == nil {
				storedAt = parsed.UnixMilli()
			}
			objects[item.Key] = map[string]any{"type": objectType(item.Key), "size": item.Size, "storedAt": storedAt}
		}
		if !page.IsTruncated {
			break
		}
		if page.NextContinuationToken == "" || page.NextContinuationToken == continuation {
			return map[string]any{"ok": false, "error": "R2 inventory pagination stopped progressing"}
		}
		continuation = page.NextContinuationToken
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
