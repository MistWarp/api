package runtimeinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatsClassifiesStoredData(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"blobs/assets/a":             "asset",
		"blobs/project-workspaces/w": "history",
		"projects/p.json":            "project",
		"users/u.json":               "user",
		"thumbnails/t.png":           "thumb",
	}
	for name, contents := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stats := Stats(root)
	types := stats["types"].(map[string]int64)
	if types["assets"] != 5 || types["history"] != 7 || types["projectData"] != 7 || types["database"] != 4 || types["thumbnails"] != 5 {
		t.Fatalf("unexpected type totals: %#v", types)
	}
}
