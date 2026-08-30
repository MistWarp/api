package rewrite

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Object struct {
	OID        string
	Compressed []byte
}

type TreeEntry struct {
	Mode string
	Name string
	OID  string
}

type CommitInput struct {
	Tree      string
	Parents   []string
	Author    string
	Timestamp int64
	Message   string
}

type GeneratedHistoryOptions struct {
	ProjectJSONPath   string
	AssetsPath        string
	OutputPath        string
	ProjectID         string
	Owner             string
	CreatedAtMillis   float64
	ParentArchivePath string
	ParentHead        string
}

type PruneDeltaResult struct {
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
	RemovedObjects int    `json:"removedObjects"`
	KeptObjects    int    `json:"keptObjects"`
}

func validOID(oid string) bool {
	if len(oid) != 40 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}

func EncodeObject(kind string, body []byte) (Object, error) {
	switch kind {
	case "blob", "tree", "commit", "tag":
	default:
		return Object{}, fmt.Errorf("unsupported Git object type %q", kind)
	}
	header := []byte(fmt.Sprintf("%s %d\x00", kind, len(body)))
	raw := make([]byte, 0, len(header)+len(body))
	raw = append(raw, header...)
	raw = append(raw, body...)
	digest := sha1.Sum(raw)
	var compressed bytes.Buffer
	compressor := zlib.NewWriter(&compressed)
	if _, err := compressor.Write(raw); err != nil {
		return Object{}, err
	}
	if err := compressor.Close(); err != nil {
		return Object{}, err
	}
	return Object{OID: hex.EncodeToString(digest[:]), Compressed: compressed.Bytes()}, nil
}

func EncodeTree(entries []TreeEntry) (Object, error) {
	entries = append([]TreeEntry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i].Name, entries[j].Name
		if entries[i].Mode == "40000" || entries[i].Mode == "040000" {
			left += "/"
		}
		if entries[j].Mode == "40000" || entries[j].Mode == "040000" {
			right += "/"
		}
		return left < right
	})
	var body bytes.Buffer
	for _, entry := range entries {
		if !validTreeMode(entry.Mode) || entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\\x00") {
			return Object{}, errors.New("invalid Git tree entry")
		}
		if !validOID(entry.OID) {
			return Object{}, errors.New("invalid Git tree object ID")
		}
		decoded, _ := hex.DecodeString(entry.OID)
		body.WriteString(entry.Mode)
		body.WriteByte(' ')
		body.WriteString(entry.Name)
		body.WriteByte(0)
		body.Write(decoded)
	}
	return EncodeObject("tree", body.Bytes())
}

func validTreeMode(mode string) bool {
	switch mode {
	case "100644", "100755", "120000", "160000", "40000", "040000":
		return true
	default:
		return false
	}
}

func EncodeCommit(input CommitInput) (Object, error) {
	if !validOID(input.Tree) {
		return Object{}, errors.New("invalid commit tree")
	}
	for _, parent := range input.Parents {
		if !validOID(parent) {
			return Object{}, errors.New("invalid commit parent")
		}
	}
	timestamp := input.Timestamp
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}
	author := strings.Map(func(character rune) rune {
		if character <= ' ' || character == '<' || character == '>' {
			return '-'
		}
		return character
	}, input.Author)
	identity := fmt.Sprintf("MistWarp <%s@mistwarp.local> %d +0000", author, timestamp)
	var body strings.Builder
	body.WriteString("tree ")
	body.WriteString(input.Tree)
	body.WriteByte('\n')
	for _, parent := range input.Parents {
		body.WriteString("parent ")
		body.WriteString(parent)
		body.WriteByte('\n')
	}
	body.WriteString("author ")
	body.WriteString(identity)
	body.WriteByte('\n')
	body.WriteString("committer ")
	body.WriteString(identity)
	body.WriteString("\n\n")
	body.WriteString(input.Message)
	body.WriteByte('\n')
	return EncodeObject("commit", []byte(body.String()))
}

// RewriteCommitParents creates a new commit object while preserving the
// original tree, identity headers, extra headers, and message.
func RewriteCommitParents(commitBody []byte, parents []string) (Object, error) {
	for _, parent := range parents {
		if !validOID(parent) {
			return Object{}, errors.New("invalid commit parent")
		}
	}
	headers, message, found := bytes.Cut(commitBody, []byte("\n\n"))
	if !found {
		return Object{}, errors.New("invalid commit body")
	}
	lines := bytes.Split(headers, []byte{'\n'})
	if len(lines) == 0 || !bytes.HasPrefix(lines[0], []byte("tree ")) || !validOID(strings.TrimPrefix(string(lines[0]), "tree ")) {
		return Object{}, errors.New("commit has no tree")
	}
	var rewritten bytes.Buffer
	inserted := false
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("parent ")) {
			if !inserted {
				writeParents(&rewritten, parents)
				inserted = true
			}
			continue
		}
		rewritten.Write(line)
		rewritten.WriteByte('\n')
		if !inserted && bytes.HasPrefix(line, []byte("tree ")) {
			writeParents(&rewritten, parents)
			inserted = true
		}
	}
	rewritten.WriteByte('\n')
	rewritten.Write(message)
	return EncodeObject("commit", rewritten.Bytes())
}

func writeParents(target *bytes.Buffer, parents []string) {
	for _, parent := range parents {
		target.WriteString("parent ")
		target.WriteString(parent)
		target.WriteByte('\n')
	}
}

func addZipBytes(writer *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o644)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

// PruneDeltaArchive removes loose Git objects already available in the
// materialized base. Non-object entries such as mwp.json, HEAD, and refs are
// copied unchanged so the result remains a standalone delta layer.
func PruneDeltaArchive(basePath, deltaPath, outputPath string) PruneDeltaResult {
	base, err := zip.OpenReader(basePath)
	if err != nil {
		return PruneDeltaResult{Error: "base archive is unavailable"}
	}
	defer base.Close()
	existing := make(map[string]struct{})
	for _, item := range base.File {
		if strings.HasPrefix(item.Name, ".git/objects/") && !item.FileInfo().IsDir() {
			existing[item.Name] = struct{}{}
		}
	}
	delta, err := zip.OpenReader(deltaPath)
	if err != nil {
		return PruneDeltaResult{Error: "delta archive is unavailable"}
	}
	defer delta.Close()
	output, err := os.Create(outputPath)
	if err != nil {
		return PruneDeltaResult{Error: "could not create pruned delta"}
	}
	writer := zip.NewWriter(output)
	result := PruneDeltaResult{}
	seen := make(map[string]struct{})
	for _, item := range delta.File {
		if _, duplicate := seen[item.Name]; duplicate {
			err = errors.New("delta archive contains duplicate paths")
			break
		}
		seen[item.Name] = struct{}{}
		isObject := strings.HasPrefix(item.Name, ".git/objects/") && !item.FileInfo().IsDir()
		if isObject {
			if _, inherited := existing[item.Name]; inherited {
				result.RemovedObjects++
				continue
			}
			result.KeptObjects++
		}
		source, openErr := item.Open()
		if openErr != nil {
			err = openErr
			break
		}
		header := item.FileHeader
		destination, createErr := writer.CreateHeader(&header)
		if createErr == nil {
			_, createErr = io.Copy(destination, source)
		}
		closeErr := source.Close()
		if createErr != nil {
			err = createErr
			break
		}
		if closeErr != nil {
			err = closeErr
			break
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
		result.Error = "could not prune delta archive"
		return result
	}
	result.OK = true
	return result
}

func PruneDeltaArchiveJSON(basePath, deltaPath, outputPath string) string {
	encoded, _ := json.Marshal(PruneDeltaArchive(basePath, deltaPath, outputPath))
	return string(encoded)
}

func readLegacyProject(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	return data, closeErr
}

func buildSB3(projectJSON []byte, assetsPath string) ([]byte, error) {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	if err := addZipBytes(writer, "project.json", projectJSON); err != nil {
		return nil, err
	}
	assets, err := os.ReadDir(assetsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, asset := range assets {
		if asset.IsDir() {
			return nil, errors.New("asset directory contains a subdirectory")
		}
		data, err := os.ReadFile(filepath.Join(assetsPath, asset.Name()))
		if err != nil {
			return nil, err
		}
		if err := addZipBytes(writer, asset.Name(), data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func copyParentObjects(path string, objects map[string][]byte) error {
	if path == "" {
		return nil
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, item := range reader.File {
		if !strings.HasPrefix(item.Name, ".git/objects/") || item.FileInfo().IsDir() {
			continue
		}
		source, err := item.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(source)
		closeErr := source.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		objects[item.Name] = data
	}
	return nil
}

func WriteGeneratedHistoryArchive(options GeneratedHistoryOptions) (string, error) {
	projectJSON, err := readLegacyProject(options.ProjectJSONPath)
	if err != nil {
		return "", err
	}
	sb3, err := buildSB3(projectJSON, options.AssetsPath)
	if err != nil {
		return "", err
	}
	objects := make(map[string][]byte)
	if err := copyParentObjects(options.ParentArchivePath, objects); err != nil {
		return "", err
	}
	blob, err := EncodeObject("blob", sb3)
	if err != nil {
		return "", err
	}
	tree, err := EncodeTree([]TreeEntry{{Mode: "100644", Name: "project.sb3", OID: blob.OID}})
	if err != nil {
		return "", err
	}
	parents := []string{}
	if options.ParentHead != "" {
		parents = append(parents, options.ParentHead)
	}
	commit, err := EncodeCommit(CommitInput{
		Tree: tree.OID, Parents: parents, Author: options.Owner,
		Timestamp: int64(options.CreatedAtMillis / 1000), Message: "Initial version",
	})
	if err != nil {
		return "", err
	}
	objects[".git/objects/"+blob.OID[:2]+"/"+blob.OID[2:]] = blob.Compressed
	objects[".git/objects/"+tree.OID[:2]+"/"+tree.OID[2:]] = tree.Compressed
	objects[".git/objects/"+commit.OID[:2]+"/"+commit.OID[2:]] = commit.Compressed
	manifest, err := generatedManifest(options, commit.OID, parents)
	if err != nil {
		return "", err
	}
	if err := writeArchive(options.OutputPath, commit.OID, manifest, objects); err != nil {
		_ = os.Remove(options.OutputPath)
		return "", err
	}
	return commit.OID, nil
}

func generatedManifest(options GeneratedHistoryOptions, head string, parents []string) ([]byte, error) {
	baseCommit := any(nil)
	if options.ParentHead != "" {
		baseCommit = options.ParentHead
	}
	commit := map[string]any{"sha": head, "message": "Initial version", "author": options.Owner, "date": options.CreatedAtMillis}
	node := map[string]any{"sha": head, "message": "Initial version", "author": options.Owner, "date": options.CreatedAtMillis, "parents": parents, "branches": []string{"main"}}
	return json.Marshal(map[string]any{
		"format": "mistwarp-project", "version": 1, "createdWith": "MistWarp API", "projectId": options.ProjectID,
		"remixParent": nil, "baseCommit": baseCommit, "branch": "main", "head": head, "worktree": false,
		"baseHead": nil, "delta": false, "commits": []any{commit},
		"graph": map[string]any{"branches": []string{"main"}, "branchLogs": []any{map[string]any{"branch": "main", "oids": []string{head}}}, "nodes": []any{node}},
	})
}

func writeArchive(path, head string, manifest []byte, objects map[string][]byte) error {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	writes := []struct {
		name string
		data []byte
	}{
		{"mwp.json", manifest},
		{".git/HEAD", []byte("ref: refs/heads/main\n")},
		{".git/refs/heads/main", []byte(head + "\n")},
		{".git/config", []byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n")},
	}
	for _, item := range writes {
		if err == nil {
			err = addZipBytes(writer, item.name, item.data)
		}
	}
	for name, data := range objects {
		if err == nil {
			err = addZipBytes(writer, name, data)
		}
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	return err
}

// WriteGeneratedHistoryArchiveJSON is the narrow OSL boundary. It returns the
// generated head, or an empty string on failure to preserve the existing API.
func WriteGeneratedHistoryArchiveJSON(projectJSONPath, assetsPath, outputPath, projectID, owner string, createdAtMillis float64, parentArchivePath, parentHead string) string {
	head, err := WriteGeneratedHistoryArchive(GeneratedHistoryOptions{
		ProjectJSONPath: projectJSONPath, AssetsPath: assetsPath, OutputPath: outputPath,
		ProjectID: projectID, Owner: owner, CreatedAtMillis: createdAtMillis,
		ParentArchivePath: parentArchivePath, ParentHead: parentHead,
	})
	if err != nil {
		return ""
	}
	return head
}
