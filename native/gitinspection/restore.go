package gitinspection

import (
	"archive/zip"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"mistwarp.local/gitinspection/rewrite"
)

type restoreResult struct {
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Head     string         `json:"head,omitempty"`
	Manifest map[string]any `json:"manifest,omitempty"`
}

func writeRestoreDelta(path, branch, head string, object rewrite.Object, manifest map[string]any) error {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	manifestJSON, err := json.Marshal(manifest)
	entries := []struct {
		name string
		data []byte
	}{
		{"mwp.json", manifestJSON},
		{".git/HEAD", []byte("ref: refs/heads/" + branch + "\n")},
		{".git/refs/heads/" + branch, []byte(head + "\n")},
		{".git/config", []byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n")},
		{".git/objects/" + object.OID[:2] + "/" + object.OID[2:], object.Compressed},
	}
	for _, entry := range entries {
		if err != nil {
			break
		}
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(0o644)
		var destination io.Writer
		destination, err = writer.CreateHeader(header)
		if err == nil {
			_, err = destination.Write(entry.data)
		}
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}

func validRestoreOID(oid string) bool {
	if len(oid) != 40 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

// RestoreArchive creates a one-commit delta whose tree matches an existing
// reachable commit. The current commit remains its sole parent, so restoring a
// version never rewrites or discards later history.
func RestoreArchive(workspacePath, currentHead, restoreHead, outputPath, projectID, remixParent, baseCommit, branch, author, message string, timestamp int64) restoreResult {
	reader, source, err := openArchive(workspacePath)
	if err != nil {
		return restoreResult{Error: "workspace archive is unavailable"}
	}
	defer reader.Close()
	if !validRestoreOID(currentHead) || !validRestoreOID(restoreHead) || !source.reachable(restoreHead, currentHead) {
		return restoreResult{Error: "restore commit is not reachable from the current history"}
	}
	selected, ok := source.parseCommit(restoreHead)
	if !ok {
		return restoreResult{Error: "restore commit is unavailable"}
	}
	if branch == "" || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return restoreResult{Error: "invalid branch"}
	}
	if message == "" {
		message = "Restored version " + restoreHead[:7]
	}
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}
	commit, err := rewrite.EncodeCommit(rewrite.CommitInput{
		Tree: selected.Tree, Parents: []string{currentHead}, Author: author, Timestamp: timestamp, Message: message,
	})
	if err != nil {
		return restoreResult{Error: "could not create restore commit"}
	}
	date := timestamp * 1000
	commitMetadata := map[string]any{"sha": commit.OID, "message": message, "author": author, "date": date}
	node := map[string]any{
		"sha": commit.OID, "message": message, "author": author, "date": date,
		"parents": []string{currentHead}, "branches": []string{branch},
	}
	manifest := map[string]any{
		"format": "mistwarp-project", "version": 1, "createdWith": "MistWarp API",
		"projectId": projectID, "remixParent": nil, "baseCommit": nil,
		"branch": branch, "head": commit.OID, "worktree": false,
		"baseHead": currentHead, "delta": true,
		"commits": []any{commitMetadata},
		"graph": map[string]any{
			"branches":   []string{branch},
			"branchLogs": []any{map[string]any{"branch": branch, "oids": []string{commit.OID, currentHead}}},
			"nodes":      []any{node},
		},
	}
	if remixParent != "" {
		manifest["remixParent"] = remixParent
	}
	if baseCommit != "" {
		manifest["baseCommit"] = baseCommit
	}
	if err := writeRestoreDelta(outputPath, branch, commit.OID, commit, manifest); err != nil {
		return restoreResult{Error: "could not write restore history"}
	}
	return restoreResult{OK: true, Head: commit.OID, Manifest: manifest}
}

func RestoreArchiveJSON(workspacePath, currentHead, restoreHead, outputPath, projectID, remixParent, baseCommit, branch, author, message string, timestamp int64) string {
	result := RestoreArchive(workspacePath, currentHead, restoreHead, outputPath, projectID, remixParent, baseCommit, branch, author, message, timestamp)
	encoded, _ := json.Marshal(result)
	return string(encoded)
}
