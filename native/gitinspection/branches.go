package gitinspection

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

var branchNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

func validBranchName(name string) bool {
	if !branchNamePattern.MatchString(name) || strings.Contains(name, "..") || strings.Contains(name, "//") || strings.HasSuffix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if name, ok := item.(string); ok {
			result = append(result, name)
		}
	}
	return result
}

func branchLogMap(graph map[string]any) map[string][]string {
	result := map[string][]string{}
	logs, _ := graph["branchLogs"].([]any)
	for _, item := range logs {
		log, _ := item.(map[string]any)
		name, _ := log["branch"].(string)
		if name != "" {
			result[name] = stringSlice(log["oids"])
		}
	}
	return result
}

func rewriteNodeBranches(graph map[string]any, action, branch, name string, sourceOIDs []string) {
	source := map[string]bool{}
	for _, oid := range sourceOIDs {
		source[oid] = true
	}
	nodes, _ := graph["nodes"].([]any)
	for _, item := range nodes {
		node, _ := item.(map[string]any)
		branches := stringSlice(node["branches"])
		rewritten := make([]any, 0, len(branches)+1)
		seen := map[string]bool{}
		for _, candidate := range branches {
			if action == "rename" && candidate == branch {
				candidate = name
			}
			if action == "delete" && candidate == branch {
				continue
			}
			if candidate != "" && !seen[candidate] {
				rewritten = append(rewritten, candidate)
				seen[candidate] = true
			}
		}
		sha, _ := node["sha"].(string)
		if action == "create" && source[sha] && !seen[name] {
			rewritten = append(rewritten, name)
		}
		node["branches"] = rewritten
	}
}

func writeBranchArchive(reader *zip.ReadCloser, outputPath string, manifest map[string]any, branches []string, logs map[string][]string) error {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	output, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	for _, item := range reader.File {
		if item.Name == "mwp.json" || item.Name == ".git/HEAD" || strings.HasPrefix(item.Name, ".git/refs/heads/") || item.FileInfo().IsDir() {
			continue
		}
		from, openErr := item.Open()
		if openErr != nil {
			err = openErr
			break
		}
		header := item.FileHeader
		to, createErr := writer.CreateHeader(&header)
		if createErr == nil {
			_, createErr = io.Copy(to, from)
		}
		_ = from.Close()
		if createErr != nil {
			err = createErr
			break
		}
	}
	active, _ := manifest["branch"].(string)
	entries := []struct {
		name string
		data []byte
	}{
		{"mwp.json", manifestJSON},
		{".git/HEAD", []byte("ref: refs/heads/" + active + "\n")},
	}
	for _, branch := range branches {
		entries = append(entries, struct {
			name string
			data []byte
		}{".git/refs/heads/" + branch, []byte(logs[branch][0] + "\n")})
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
	}
	return err
}

// ManageBranchArchive applies branch-only changes without changing any commit
// or worktree object. It writes a full archive so removed refs cannot survive in
// an older workspace layer.
func ManageBranchArchive(sourcePath, outputPath, action, branch, name, from, historyJSON string) restoreResult {
	reader, _, err := openArchive(sourcePath)
	if err != nil {
		return restoreResult{Error: "workspace archive is unavailable"}
	}
	defer reader.Close()
	var manifest map[string]any
	for _, item := range reader.File {
		if item.Name != "mwp.json" {
			continue
		}
		file, openErr := item.Open()
		if openErr != nil {
			return restoreResult{Error: "workspace manifest is unavailable"}
		}
		manifest = map[string]any{}
		decodeErr := json.NewDecoder(file).Decode(&manifest)
		_ = file.Close()
		if decodeErr != nil {
			return restoreResult{Error: "workspace manifest is invalid"}
		}
		break
	}
	if manifest == nil {
		return restoreResult{Error: "workspace manifest is unavailable"}
	}
	if historyJSON != "" {
		history := map[string]any{}
		if err := json.Unmarshal([]byte(historyJSON), &history); err != nil {
			return restoreResult{Error: "project branch history is invalid"}
		}
		manifest["branch"] = history["branch"]
		manifest["graph"] = history["graph"]
		manifest["commits"] = history["commits"]
		if head, ok := history["head"].(string); ok && head != "" {
			manifest["head"] = head
		}
	}
	graph, _ := manifest["graph"].(map[string]any)
	if graph == nil {
		return restoreResult{Error: "workspace branch history is unavailable"}
	}
	branches := stringSlice(graph["branches"])
	logs := branchLogMap(graph)
	contains := func(candidate string) bool {
		for _, existing := range branches {
			if existing == candidate {
				return true
			}
		}
		return false
	}
	active, _ := manifest["branch"].(string)
	if !contains(active) || len(logs[active]) == 0 {
		return restoreResult{Error: "workspace current branch is invalid"}
	}
	switch action {
	case "create":
		if !validBranchName(name) {
			return restoreResult{Error: "invalid branch name"}
		}
		if contains(name) {
			return restoreResult{Error: "branch already exists"}
		}
		if !contains(from) || len(logs[from]) == 0 {
			return restoreResult{Error: "source branch not found"}
		}
		branches = append(branches, name)
		logs[name] = append([]string{}, logs[from]...)
		rewriteNodeBranches(graph, action, "", name, logs[from])
	case "rename":
		if !contains(branch) || len(logs[branch]) == 0 {
			return restoreResult{Error: "branch not found"}
		}
		if !validBranchName(name) {
			return restoreResult{Error: "invalid branch name"}
		}
		if name != branch && contains(name) {
			return restoreResult{Error: "branch already exists"}
		}
		if name == branch {
			return restoreResult{Error: "choose a different branch name"}
		}
		for index, existing := range branches {
			if existing == branch {
				branches[index] = name
			}
		}
		logs[name] = logs[branch]
		delete(logs, branch)
		rewriteNodeBranches(graph, action, branch, name, nil)
		if active == branch {
			active = name
		}
	case "delete":
		if !contains(branch) {
			return restoreResult{Error: "branch not found"}
		}
		if branch == active {
			return restoreResult{Error: "the current branch cannot be deleted"}
		}
		if len(branches) <= 1 {
			return restoreResult{Error: "the last branch cannot be deleted"}
		}
		kept := make([]string, 0, len(branches)-1)
		for _, existing := range branches {
			if existing != branch {
				kept = append(kept, existing)
			}
		}
		branches = kept
		delete(logs, branch)
		rewriteNodeBranches(graph, action, branch, "", nil)
	default:
		return restoreResult{Error: "invalid branch action"}
	}
	branchLogs := make([]any, 0, len(branches))
	for _, branch := range branches {
		if len(logs[branch]) == 0 || !validRestoreOID(logs[branch][0]) {
			return restoreResult{Error: "workspace branch history is invalid"}
		}
		oids := make([]any, len(logs[branch]))
		for index, oid := range logs[branch] {
			oids[index] = oid
		}
		branchLogs = append(branchLogs, map[string]any{"branch": branch, "oids": oids})
	}
	branchValues := make([]any, len(branches))
	for index, branch := range branches {
		branchValues[index] = branch
	}
	graph["branches"] = branchValues
	graph["branchLogs"] = branchLogs
	manifest["branch"] = active
	manifest["head"] = logs[active][0]
	manifest["baseHead"] = nil
	manifest["delta"] = false
	if err := writeBranchArchive(reader, outputPath, manifest, branches, logs); err != nil {
		return restoreResult{Error: "could not write branch history"}
	}
	return restoreResult{OK: true, Head: logs[active][0], Manifest: manifest}
}

func ManageBranchArchiveJSON(sourcePath, outputPath, action, branch, name, from, historyJSON string) string {
	result := ManageBranchArchive(sourcePath, outputPath, action, branch, name, from, historyJSON)
	encoded, _ := json.Marshal(result)
	return string(encoded)
}
