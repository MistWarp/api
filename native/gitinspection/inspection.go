package gitinspection

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"
)

const (
	maxObjectBytes          = 256 << 20
	maxTreeDepth            = 64
	maxTreeFiles            = 20_000
	maxReachable            = 10_000
	maxInlineDiffFileBytes  = 1 << 20
	maxInlineDiffTotalBytes = 8 << 20
)

type gitFile struct {
	Path   string `json:"path"`
	OID    string `json:"oid"`
	Mode   string `json:"mode"`
	Size   int    `json:"size"`
	Binary bool   `json:"binary"`
}

type gitCommit struct {
	Tree    string   `json:"tree"`
	Parents []string `json:"parents"`
	Author  string   `json:"author"`
	Date    int64    `json:"date"`
	Message string   `json:"message"`
}

type archive struct {
	objects map[string]*zip.File
}

func failure(message string) string {
	encoded, _ := json.Marshal(map[string]any{"ok": false, "error": message})
	return string(encoded)
}

func openArchive(path string) (*zip.ReadCloser, archive, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, archive{}, err
	}
	result := archive{objects: make(map[string]*zip.File)}
	for _, item := range reader.File {
		if strings.HasPrefix(item.Name, ".git/objects/") && !item.FileInfo().IsDir() {
			result.objects[item.Name] = item
		}
	}
	return reader, result, nil
}

func (a archive) readObject(oid string) (string, []byte, bool) {
	if len(oid) != 40 {
		return "", nil, false
	}
	item := a.objects[".git/objects/"+oid[:2]+"/"+oid[2:]]
	if item == nil || item.UncompressedSize64 > maxObjectBytes {
		return "", nil, false
	}
	source, err := item.Open()
	if err != nil {
		return "", nil, false
	}
	compressed, readErr := io.ReadAll(io.LimitReader(source, maxObjectBytes+1))
	closeErr := source.Close()
	if readErr != nil || closeErr != nil || len(compressed) > maxObjectBytes {
		return "", nil, false
	}
	inflater, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", nil, false
	}
	objectData, readErr := io.ReadAll(io.LimitReader(inflater, maxObjectBytes+1))
	closeErr = inflater.Close()
	if readErr != nil || closeErr != nil || len(objectData) > maxObjectBytes {
		return "", nil, false
	}
	separator := bytes.IndexByte(objectData, 0)
	if separator < 1 {
		return "", nil, false
	}
	header := string(objectData[:separator])
	space := strings.IndexByte(header, ' ')
	if space < 1 {
		return "", nil, false
	}
	kind := header[:space]
	declared, err := strconv.ParseInt(header[space+1:], 10, 64)
	body := objectData[separator+1:]
	if err != nil || declared != int64(len(body)) {
		return "", nil, false
	}
	digest := sha1.Sum(objectData)
	if hex.EncodeToString(digest[:]) != oid {
		return "", nil, false
	}
	return kind, body, true
}

func (a archive) parseCommit(oid string) (gitCommit, bool) {
	kind, body, ok := a.readObject(oid)
	if !ok || kind != "commit" {
		return gitCommit{}, false
	}
	text := string(body)
	headers, message, found := strings.Cut(text, "\n\n")
	if !found {
		headers = text
		message = ""
	}
	result := gitCommit{Parents: []string{}, Message: strings.TrimSuffix(message, "\n")}
	for _, line := range strings.Split(headers, "\n") {
		switch {
		case strings.HasPrefix(line, "tree "):
			result.Tree = line[5:]
		case strings.HasPrefix(line, "parent "):
			result.Parents = append(result.Parents, line[7:])
		case strings.HasPrefix(line, "author "):
			author := line[7:]
			if end := strings.LastIndex(author, " <"); end > 0 {
				result.Author = author[:end]
			} else {
				result.Author = author
			}
			fields := strings.Fields(author)
			if len(fields) >= 2 {
				if parsed, err := strconv.ParseInt(fields[len(fields)-2], 10, 64); err == nil {
					result.Date = parsed * 1000
				}
			}
		}
	}
	if len(result.Tree) != 40 {
		return gitCommit{}, false
	}
	return result, true
}

func (a archive) walkTree(rootOID string) ([]gitFile, bool) {
	files := []gitFile{}
	active := make(map[string]bool)
	var walk func(string, string, int) bool
	walk = func(treeOID, prefix string, depth int) bool {
		if depth > maxTreeDepth || active[treeOID] {
			return false
		}
		active[treeOID] = true
		defer delete(active, treeOID)
		kind, body, ok := a.readObject(treeOID)
		if !ok || kind != "tree" {
			return false
		}
		position := 0
		for position < len(body) {
			space := bytes.IndexByte(body[position:], ' ')
			if space < 1 {
				return false
			}
			space += position
			nul := bytes.IndexByte(body[space+1:], 0)
			if nul < 1 {
				return false
			}
			nul += space + 1
			if nul+21 > len(body) {
				return false
			}
			mode := string(body[position:space])
			name := string(body[space+1 : nul])
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				return false
			}
			childOID := hex.EncodeToString(body[nul+1 : nul+21])
			fullPath := name
			if prefix != "" {
				fullPath = prefix + "/" + name
			}
			position = nul + 21
			if mode == "40000" || mode == "040000" {
				if !walk(childOID, fullPath, depth+1) {
					return false
				}
				continue
			}
			blobKind, blob, ok := a.readObject(childOID)
			if !ok || blobKind != "blob" {
				return false
			}
			probe := blob
			if len(probe) > 8000 {
				probe = probe[:8000]
			}
			files = append(files, gitFile{
				Path: fullPath, OID: childOID, Mode: mode, Size: len(blob), Binary: bytes.IndexByte(probe, 0) >= 0,
			})
			if len(files) > maxTreeFiles {
				return false
			}
		}
		return position == len(body)
	}
	return files, walk(rootOID, "", 0)
}

func (a archive) reachable(sha, allowedHeads string) bool {
	queue := []string{}
	for _, candidate := range strings.Split(allowedHeads, ",") {
		if len(candidate) == 40 {
			queue = append(queue, candidate)
		}
	}
	visited := make(map[string]bool)
	for len(queue) > 0 && len(visited) <= maxReachable {
		candidate := queue[0]
		queue = queue[1:]
		if visited[candidate] {
			continue
		}
		visited[candidate] = true
		if candidate == sha {
			return true
		}
		commit, ok := a.parseCommit(candidate)
		if ok {
			queue = append(queue, commit.Parents...)
		}
	}
	return false
}

func encode(value map[string]any, message string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return failure(message)
	}
	return string(encoded)
}

// Inspect reads a commit, tree, or file from the loose Git objects stored in a
// MistWarp project archive. It returns the JSON contract consumed by the OSL API.
func Inspect(workspacePath, sha, operation, requestedPath, allowedHeads string) string {
	reader, archive, err := openArchive(workspacePath)
	if err != nil {
		return failure("workspace archive is unavailable")
	}
	defer reader.Close()

	commit, ok := archive.parseCommit(sha)
	if !ok {
		return failure("commit was not found in this workspace")
	}
	if !archive.reachable(sha, allowedHeads) {
		return failure("commit is not reachable from this project's history")
	}

	switch operation {
	case "tree":
		files, ok := archive.walkTree(commit.Tree)
		if !ok {
			return failure("commit tree is invalid")
		}
		return encode(map[string]any{"ok": true, "sha": sha, "commit": commit, "files": files}, "could not encode commit tree")
	case "file":
		files, ok := archive.walkTree(commit.Tree)
		if !ok {
			return failure("commit tree is invalid")
		}
		for _, file := range files {
			if file.Path != requestedPath {
				continue
			}
			kind, content, found := archive.readObject(file.OID)
			if !found || kind != "blob" {
				return failure("file object is unavailable")
			}
			return encode(map[string]any{"ok": true, "sha": sha, "file": file, "content": content}, "could not encode file")
		}
		return failure("file was not found at this commit")
	default:
		return archive.inspectCommit(sha, commit)
	}
}

// CompareArchives compares two commits without sending either repository to
// the browser. The commits may live in separate archives, as they do for pull
// requests between a project and its remix.
func CompareArchives(basePath, baseSHA, headPath, headSHA string) string {
	baseReader, baseArchive, err := openArchive(basePath)
	if err != nil {
		return failure("base workspace archive is unavailable")
	}
	defer baseReader.Close()
	headReader, headArchive, err := openArchive(headPath)
	if err != nil {
		return failure("head workspace archive is unavailable")
	}
	defer headReader.Close()

	baseCommit, ok := baseArchive.parseCommit(baseSHA)
	if !ok {
		return failure("base commit is unavailable")
	}
	headCommit, ok := headArchive.parseCommit(headSHA)
	if !ok {
		return failure("head commit is unavailable")
	}
	return compareCommitTrees(baseArchive, baseSHA, baseCommit, headArchive, headSHA, headCommit)
}

func CompareArchivesJSON(basePath, baseSHA, headPath, headSHA string) string {
	return CompareArchives(basePath, baseSHA, headPath, headSHA)
}

func (a archive) inspectCommit(sha string, commit gitCommit) string {
	parent := ""
	if len(commit.Parents) > 0 {
		parent = commit.Parents[0]
		parentCommit, ok := a.parseCommit(parent)
		if !ok {
			return failure("parent commit is unavailable")
		}
		return compareCommitTrees(a, parent, parentCommit, a, sha, commit)
	}
	return compareCommitTrees(archive{objects: map[string]*zip.File{}}, "", gitCommit{}, a, sha, commit)
}

func textDiffPath(name string) bool {
	lower := strings.ToLower(name)
	base := lower
	if slash := strings.LastIndexByte(base, '/'); slash >= 0 {
		base = base[slash+1:]
	}
	if base == ".gitignore" || base == "readme" || strings.HasPrefix(base, "readme.") {
		return true
	}
	for _, extension := range []string{
		".fractch", ".json", ".txt", ".md", ".js", ".jsx", ".ts", ".tsx",
		".css", ".html", ".xml", ".yaml", ".yml", ".osl", ".oip",
	} {
		if strings.HasSuffix(lower, extension) {
			return true
		}
	}
	return false
}

func inlineDiffContent(changes []map[string]any, baseArchive archive, parentFiles map[string]gitFile, headArchive archive, currentFiles map[string]gitFile) {
	total := 0
	for _, change := range changes {
		name, _ := change["path"].(string)
		if !textDiffPath(name) {
			continue
		}
		before, hasBefore := parentFiles[name]
		after, hasAfter := currentFiles[name]
		if (hasBefore && before.Binary) || (hasAfter && after.Binary) {
			continue
		}
		bytesNeeded := 0
		if hasBefore {
			bytesNeeded += before.Size
		}
		if hasAfter {
			bytesNeeded += after.Size
		}
		if bytesNeeded > maxInlineDiffFileBytes || total+bytesNeeded > maxInlineDiffTotalBytes {
			continue
		}
		if hasBefore {
			kind, content, ok := baseArchive.readObject(before.OID)
			if !ok || kind != "blob" {
				continue
			}
			change["oldData"] = content
		}
		if hasAfter {
			kind, content, ok := headArchive.readObject(after.OID)
			if !ok || kind != "blob" {
				delete(change, "oldData")
				continue
			}
			change["newData"] = content
		}
		total += bytesNeeded
	}
}

func compareCommitTrees(baseArchive archive, baseSHA string, baseCommit gitCommit, headArchive archive, headSHA string, headCommit gitCommit) string {
	parentFiles := make(map[string]gitFile)
	parentTree := ""
	if baseSHA != "" {
		listed, ok := baseArchive.walkTree(baseCommit.Tree)
		if !ok {
			return failure("parent commit tree is invalid")
		}
		parentTree = baseCommit.Tree
		for _, file := range listed {
			parentFiles[file.Path] = file
		}
	}
	currentFiles, ok := headArchive.walkTree(headCommit.Tree)
	if !ok {
		return failure("commit tree is invalid")
	}
	current := make(map[string]gitFile, len(currentFiles))
	for _, file := range currentFiles {
		current[file.Path] = file
	}
	changes := []map[string]any{}
	additions, deletions := 0, 0
	legacy := false
	for path, before := range parentFiles {
		after, exists := current[path]
		if !exists {
			changes = append(changes, map[string]any{"path": path, "status": "removed", "oldOid": before.OID, "newOid": nil, "oldSize": before.Size, "newSize": 0})
			deletions++
			legacy = legacy || path == "project.sb3"
		} else if before.OID != after.OID {
			changes = append(changes, map[string]any{"path": path, "status": "modified", "oldOid": before.OID, "newOid": after.OID, "oldSize": before.Size, "newSize": after.Size})
			additions++
			deletions++
			legacy = legacy || path == "project.sb3"
		}
	}
	for path, after := range current {
		if _, exists := parentFiles[path]; exists {
			continue
		}
		changes = append(changes, map[string]any{"path": path, "status": "added", "oldOid": nil, "newOid": after.OID, "oldSize": 0, "newSize": after.Size})
		additions++
		legacy = legacy || path == "project.sb3"
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i]["path"].(string) < changes[j]["path"].(string)
	})
	inlineDiffContent(changes, baseArchive, parentFiles, headArchive, current)
	return encode(map[string]any{
		"ok": true, "sha": headSHA, "parent": baseSHA, "tree": headCommit.Tree, "parentTree": parentTree, "commit": headCommit, "files": changes,
		"additions": additions, "deletions": deletions, "legacy": legacy,
	}, "could not encode commit inspection")
}
