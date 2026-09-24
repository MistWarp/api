package storagefs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cacheMaxAge   = 24 * time.Hour
	cacheMaxFiles = 256
	cacheMaxBytes = 2147483648
)

func Missing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func Bytes(text string) []byte {
	return []byte(text)
}

func UniqueTimestamp() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func ConcatenateFiles(outputPath string, inputPaths []any, expectedBytes float64) bool {
	output, err := os.Create(outputPath)
	if err != nil {
		return false
	}
	var written int64
	ok := true
	for _, value := range inputPaths {
		path, isString := value.(string)
		if !isString {
			ok = false
			break
		}
		input, openErr := os.Open(path)
		if openErr != nil {
			ok = false
			break
		}
		copied, copyErr := io.Copy(output, input)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			ok = false
			break
		}
		written += copied
	}
	closeErr := output.Close()
	if !ok || closeErr != nil || written != int64(expectedBytes) {
		os.Remove(outputPath)
		return false
	}
	return true
}

type cachedFile struct {
	path     string
	name     string
	size     int64
	modified time.Time
}

func PruneCacheDir(cacheDir, preserveName string) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	files := []cachedFile{}
	var total int64
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() || strings.Contains(entry.Name(), ".tmp.") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		item := cachedFile{path: cacheDir + entry.Name(), name: entry.Name(), size: info.Size(), modified: info.ModTime()}
		if entry.Name() != preserveName && now.Sub(info.ModTime()) > cacheMaxAge {
			os.Remove(item.path)
			continue
		}
		files = append(files, item)
		total += item.size
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modified.Before(files[j].modified) })
	for (len(files) > cacheMaxFiles || total > cacheMaxBytes) && len(files) > 0 {
		removeAt := -1
		for i, item := range files {
			if item.name != preserveName {
				removeAt = i
				break
			}
		}
		if removeAt < 0 {
			break
		}
		item := files[removeAt]
		os.Remove(item.path)
		total -= item.size
		files = append(files[:removeAt], files[removeAt+1:]...)
	}
}
