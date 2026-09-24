package storagefs

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
)

type entryTracker struct {
	paths        map[string]bool
	requiredDirs map[string]bool
}

func newEntryTracker() *entryTracker {
	return &entryTracker{paths: map[string]bool{}, requiredDirs: map[string]bool{}}
}

func unsafeEntryName(name string) (string, []string, bool) {
	cleanName := strings.TrimSuffix(name, "/")
	unsafe := cleanName == "" || len(name) > 1024 || strings.HasPrefix(cleanName, "/") || strings.Contains(cleanName, "\\") || strings.ContainsRune(cleanName, 0)
	parts := strings.Split(cleanName, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			unsafe = true
		}
	}
	return cleanName, parts, unsafe
}

func (tracker *entryTracker) claimParents(parts []string) bool {
	parent := ""
	for index := 0; index < len(parts)-1; index++ {
		if parent == "" {
			parent = parts[index]
		} else {
			parent += "/" + parts[index]
		}
		if parentDirectory, exists := tracker.paths[parent]; exists && !parentDirectory {
			return false
		}
		tracker.requiredDirs[parent] = true
	}
	return true
}

func storedFile(item *zip.File) bool {
	return item.Mode().IsRegular() && (item.Method == zip.Store || item.Method == zip.Deflate)
}

func read16(data []byte, offset int) uint64 {
	return uint64(data[offset]) | uint64(data[offset+1])<<8
}

func read32(data []byte, offset int) uint64 {
	return read16(data, offset) | read16(data, offset+2)<<16
}

func centralDirectoryIsConsistent(data []byte, maxFiles uint64) (uint64, bool) {
	eocd := -1
	searchStart := len(data) - 22 - 65535
	if searchStart < 0 {
		searchStart = 0
	}
	for offset := len(data) - 22; offset >= searchStart; offset-- {
		if data[offset] == 0x50 && data[offset+1] == 0x4b && data[offset+2] == 0x05 && data[offset+3] == 0x06 && offset+22+int(read16(data, offset+20)) == len(data) {
			eocd = offset
			break
		}
	}
	if eocd < 0 || read16(data, eocd+4) != 0 || read16(data, eocd+6) != 0 {
		return 0, false
	}
	entries := read16(data, eocd+10)
	if entries == 0 || entries == 65535 || read16(data, eocd+8) != entries || entries > maxFiles {
		return 0, false
	}
	directorySize := read32(data, eocd+12)
	directoryOffset := read32(data, eocd+16)
	if directoryOffset+directorySize != uint64(eocd) || directoryOffset > uint64(len(data)) {
		return 0, false
	}
	position := int(directoryOffset)
	directoryEnd := int(directoryOffset + directorySize)
	counted := uint64(0)
	for position < directoryEnd {
		if position+46 > directoryEnd || data[position] != 0x50 || data[position+1] != 0x4b || data[position+2] != 0x01 || data[position+3] != 0x02 {
			return 0, false
		}
		position += 46 + int(read16(data, position+28)) + int(read16(data, position+30)) + int(read16(data, position+32))
		counted++
		if position > directoryEnd || counted > maxFiles {
			return 0, false
		}
	}
	return entries, position == directoryEnd && counted == entries
}

func ValidateZipArchive(data []byte, maxCompressedBytes, maxExpandedBytes, maxFiles float64, readContents bool) bool {
	if len(data) < 22 || int64(len(data)) > int64(maxCompressedBytes) {
		return false
	}
	entries, ok := centralDirectoryIsConsistent(data, uint64(maxFiles))
	if !ok {
		return false
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(reader.File) != int(entries) {
		return false
	}
	tracker := newEntryTracker()
	var declaredBytes uint64
	var actualBytes uint64
	expandedLimit := uint64(maxExpandedBytes)
	for _, item := range reader.File {
		cleanName, parts, unsafe := unsafeEntryName(item.Name)
		directory := item.FileInfo().IsDir()
		if unsafe || item.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if _, exists := tracker.paths[cleanName]; exists || !directory && tracker.requiredDirs[cleanName] {
			return false
		}
		if !tracker.claimParents(parts) {
			return false
		}
		tracker.paths[cleanName] = directory
		if directory {
			if item.UncompressedSize64 != 0 {
				return false
			}
			continue
		}
		if !storedFile(item) || item.UncompressedSize64 > expandedLimit-declaredBytes {
			return false
		}
		declaredBytes += item.UncompressedSize64
		if !readContents {
			continue
		}
		source, openErr := item.Open()
		if openErr != nil {
			return false
		}
		copied, copyErr := io.Copy(io.Discard, io.LimitReader(source, int64(expandedLimit-actualBytes)+1))
		closeErr := source.Close()
		if copyErr != nil || closeErr != nil || copied < 0 || uint64(copied) != item.UncompressedSize64 || uint64(copied) > expandedLimit-actualBytes {
			return false
		}
		actualBytes += uint64(copied)
	}
	return !readContents || actualBytes == declaredBytes
}

type archiveMerger struct {
	writer        *zip.Writer
	tracker       *entryTracker
	fileCount     int
	fileLimit     int
	expandedBytes uint64
	expandedLimit uint64
}

func (merger *archiveMerger) copyArchive(sourcePath string) bool {
	reader, openErr := zip.OpenReader(sourcePath)
	if openErr != nil {
		return false
	}
	defer reader.Close()
	for _, item := range reader.File {
		if !merger.copyEntry(item) {
			return false
		}
	}
	return true
}

func (merger *archiveMerger) copyEntry(item *zip.File) bool {
	cleanName, parts, unsafe := unsafeEntryName(item.Name)
	directory := item.FileInfo().IsDir()
	if unsafe || item.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if _, exists := merger.tracker.paths[cleanName]; exists {
		return true
	}
	if !directory && merger.tracker.requiredDirs[cleanName] {
		return false
	}
	if !merger.tracker.claimParents(parts) {
		return false
	}
	merger.tracker.paths[cleanName] = directory
	merger.fileCount++
	if merger.fileCount > merger.fileLimit || item.UncompressedSize64 > merger.expandedLimit-merger.expandedBytes {
		return false
	}
	header := item.FileHeader
	if directory {
		if item.UncompressedSize64 != 0 {
			return false
		}
		_, createErr := merger.writer.CreateHeader(&header)
		return createErr == nil
	}
	if !storedFile(item) {
		return false
	}
	source, readErr := item.Open()
	if readErr != nil {
		return false
	}
	target, createErr := merger.writer.CreateHeader(&header)
	if createErr != nil {
		source.Close()
		return false
	}
	remaining := merger.expandedLimit - merger.expandedBytes
	copied, copyErr := io.Copy(target, io.LimitReader(source, int64(remaining)+1))
	closeErr := source.Close()
	if copyErr != nil || closeErr != nil || copied < 0 || uint64(copied) != item.UncompressedSize64 || uint64(copied) > remaining {
		return false
	}
	merger.expandedBytes += uint64(copied)
	return true
}

func MergeZipArchives(basePath, deltaPath, outputPath string, maxExpandedBytes, maxFiles float64) bool {
	output, err := os.Create(outputPath)
	if err != nil {
		return false
	}
	merger := &archiveMerger{
		writer:        zip.NewWriter(output),
		tracker:       newEntryTracker(),
		fileLimit:     int(maxFiles),
		expandedLimit: uint64(maxExpandedBytes),
	}
	ok := merger.copyArchive(deltaPath) && merger.copyArchive(basePath)
	closeErr := merger.writer.Close()
	fileCloseErr := output.Close()
	if !ok || closeErr != nil || fileCloseErr != nil {
		os.Remove(outputPath)
		return false
	}
	return true
}
