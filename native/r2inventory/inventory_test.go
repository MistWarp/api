package r2inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestScanListsEveryPageAndClassifiesObjects(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/mistwarp" || request.URL.Query().Get("list-type") != "2" {
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
		if request.Header.Get("Authorization") == "" {
			t.Fatal("inventory request was not signed")
		}
		response.Header().Set("Content-Type", "application/xml")
		if request.URL.Query().Get("continuation-token") == "" {
			_, _ = fmt.Fprint(response, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next page</NextContinuationToken><Contents><Key>assets/a.png</Key><LastModified>2026-08-30T10:00:00Z</LastModified><Size>12</Size></Contents></ListBucketResult>`)
			return
		}
		_, _ = fmt.Fprint(response, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>project-workspaces/a.mwp</Key><LastModified>2026-08-30T11:00:00Z</LastModified><Size>34</Size></Contents></ListBucketResult>`)
	}))
	defer server.Close()

	result := scan(context.Background(), server.URL, "mistwarp", "access", "secret", "auto", server.Client())
	if result["ok"] != true || result["pages"] != 2 || requests != 2 {
		t.Fatalf("unexpected scan result: %#v", result)
	}
	objects := result["objects"].(map[string]any)
	if objects["assets/a.png"].(map[string]any)["type"] != "assets" {
		t.Fatalf("asset was misclassified: %#v", objects)
	}
	if objects["project-workspaces/a.mwp"].(map[string]any)["type"] != "history" {
		t.Fatalf("history was misclassified: %#v", objects)
	}
	if _, err := json.Marshal(result); err != nil {
		t.Fatal(err)
	}
}
