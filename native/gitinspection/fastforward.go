package gitinspection

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"strings"
)

func rewriteFastForwardManifest(raw []byte, projectID, remixParent, baseCommit, branch, head string) (map[string]any, []byte, error) {
	manifest := map[string]any{}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, nil, err
	}
	sourceBranch, _ := manifest["branch"].(string)
	manifest["projectId"] = projectID
	manifest["remixParent"] = nil
	manifest["baseCommit"] = nil
	manifest["branch"] = branch
	manifest["head"] = head
	manifest["baseHead"] = nil
	manifest["delta"] = false
	if remixParent != "" {
		manifest["remixParent"] = remixParent
	}
	if baseCommit != "" {
		manifest["baseCommit"] = baseCommit
	}
	graph, _ := manifest["graph"].(map[string]any)
	if graph == nil {
		graph = map[string]any{}
		manifest["graph"] = graph
	}
	branches := []any{branch}
	if existing, ok := graph["branches"].([]any); ok {
		for _, candidate := range existing {
			name, _ := candidate.(string)
			if name != "" && name != branch && name != sourceBranch {
				branches = append(branches, name)
			}
		}
	}
	graph["branches"] = branches
	var activeLog []any
	var otherLogs []any
	if logs, ok := graph["branchLogs"].([]any); ok {
		for _, candidate := range logs {
			log, _ := candidate.(map[string]any)
			name, _ := log["branch"].(string)
			if name == sourceBranch {
				activeLog, _ = log["oids"].([]any)
			} else if name != branch {
				otherLogs = append(otherLogs, log)
			}
		}
	}
	if len(activeLog) == 0 {
		activeLog = []any{head}
	}
	graph["branchLogs"] = append([]any{map[string]any{"branch": branch, "oids": activeLog}}, otherLogs...)
	if nodes, ok := graph["nodes"].([]any); ok {
		for _, candidate := range nodes {
			node, _ := candidate.(map[string]any)
			sha, _ := node["sha"].(string)
			if sha == head {
				node["branches"] = []any{branch}
			}
		}
	}
	encoded, err := json.Marshal(manifest)
	return manifest, encoded, err
}

// FastForwardArchive rewrites repository metadata for a target project while
// preserving the source commit objects byte for byte.
func FastForwardArchive(sourcePath, head, outputPath, projectID, remixParent, baseCommit, branch string) restoreResult {
	reader, source, err := openArchive(sourcePath)
	if err != nil {
		return restoreResult{Error: "source workspace archive is unavailable"}
	}
	defer reader.Close()
	if !validRestoreOID(head) {
		return restoreResult{Error: "invalid fast-forward head"}
	}
	if _, ok := source.parseCommit(head); !ok {
		return restoreResult{Error: "fast-forward head is unavailable"}
	}
	if branch == "" || strings.Contains(branch, "..") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") {
		return restoreResult{Error: "invalid target branch"}
	}
	var rawManifest []byte
	for _, item := range reader.File {
		if item.Name != "mwp.json" {
			continue
		}
		file, openErr := item.Open()
		if openErr != nil {
			return restoreResult{Error: "source manifest is unavailable"}
		}
		rawManifest, err = io.ReadAll(file)
		_ = file.Close()
		break
	}
	manifest, manifestJSON, err := rewriteFastForwardManifest(rawManifest, projectID, remixParent, baseCommit, branch, head)
	if err != nil {
		return restoreResult{Error: "source manifest is invalid"}
	}
	output, err := os.Create(outputPath)
	if err != nil {
		return restoreResult{Error: "could not create fast-forward history"}
	}
	writer := zip.NewWriter(output)
	skip := map[string]bool{"mwp.json": true, ".git/HEAD": true, ".git/refs/heads/" + branch: true}
	for _, item := range reader.File {
		if skip[item.Name] || item.FileInfo().IsDir() {
			continue
		}
		sourceFile, openErr := item.Open()
		if openErr != nil {
			err = openErr
			break
		}
		header := item.FileHeader
		destination, createErr := writer.CreateHeader(&header)
		if createErr == nil {
			_, createErr = io.Copy(destination, sourceFile)
		}
		_ = sourceFile.Close()
		if createErr != nil {
			err = createErr
			break
		}
	}
	entries := []struct {
		name string
		data []byte
	}{
		{"mwp.json", manifestJSON},
		{".git/HEAD", []byte("ref: refs/heads/" + branch + "\n")},
		{".git/refs/heads/" + branch, []byte(head + "\n")},
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
		_ = os.Remove(outputPath)
		return restoreResult{Error: "could not write fast-forward history"}
	}
	return restoreResult{OK: true, Head: head, Manifest: manifest}
}

func FastForwardArchiveJSON(sourcePath, head, outputPath, projectID, remixParent, baseCommit, branch string) string {
	result := FastForwardArchive(sourcePath, head, outputPath, projectID, remixParent, baseCommit, branch)
	encoded, _ := json.Marshal(result)
	return string(encoded)
}
