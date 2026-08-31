package runtimeinfo

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

var storageTypes = []string{"assets", "history", "projectData", "database", "cache", "thumbnails", "other"}

func classify(path string) string {
	clean := filepath.ToSlash(path)
	switch {
	case strings.Contains(clean, "/blobs/assets/"):
		return "assets"
	case strings.Contains(clean, "/blobs/project-workspaces/"):
		return "history"
	case strings.Contains(clean, "/staging/"), strings.Contains(clean, "/projects/"):
		return "projectData"
	case strings.HasSuffix(clean, ".db"), strings.Contains(clean, "/users/"), strings.Contains(clean, "/comments/"):
		return "database"
	case strings.Contains(clean, "/tmp/"), strings.Contains(clean, "/git-inspection/"):
		return "cache"
	case strings.Contains(clean, "/thumbnails/"):
		return "thumbnails"
	default:
		return "other"
	}
}

func Stats(root string) map[string]any {
	types := make(map[string]int64, len(storageTypes))
	for _, storageType := range storageTypes {
		types[storageType] = 0
	}
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry == nil || entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		size := info.Size()
		total += size
		types[classify(path)] += size
		return nil
	})

	var disk syscall.Statfs_t
	_ = syscall.Statfs(root, &disk)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return map[string]any{
		"bytes":          total,
		"diskTotalBytes": int64(disk.Blocks) * int64(disk.Bsize),
		"diskFreeBytes":  int64(disk.Bavail) * int64(disk.Bsize),
		"types":          types,
		"memory": map[string]any{
			"heapBytes":   int64(memory.HeapAlloc),
			"systemBytes": int64(memory.Sys),
			"stackBytes":  int64(memory.StackInuse),
			"goroutines":  runtime.NumGoroutine(),
		},
	}
}

func StatsJSON(root string) string {
	encoded, err := json.Marshal(Stats(root))
	if err != nil {
		return `{"bytes":0,"diskTotalBytes":0,"diskFreeBytes":0,"types":{},"memory":{}}`
	}
	return string(encoded)
}
