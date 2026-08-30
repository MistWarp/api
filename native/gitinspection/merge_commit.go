package gitinspection

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"mistwarp.local/gitinspection/rewrite"
)

type treeNode struct {
	files map[string]rewrite.Object
	dirs  map[string]*treeNode
}

func newTreeNode() *treeNode {
	return &treeNode{files: map[string]rewrite.Object{}, dirs: map[string]*treeNode{}}
}

func readMergedTree(path string) (*treeNode, error) {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	root := newTreeNode()
	for _, item := range reader.File {
		if item.FileInfo().IsDir() || item.Name == "" || strings.HasPrefix(item.Name, "/") || strings.Contains(item.Name, "\\") {
			continue
		}
		parts := strings.Split(item.Name, "/")
		valid := true
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				valid = false
			}
		}
		if !valid {
			return nil, os.ErrInvalid
		}
		file, openErr := item.Open()
		if openErr != nil {
			return nil, openErr
		}
		content, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			return nil, readErr
		}
		blob, encodeErr := rewrite.EncodeObject("blob", content)
		if encodeErr != nil {
			return nil, encodeErr
		}
		node := root
		for _, directory := range parts[:len(parts)-1] {
			if node.dirs[directory] == nil {
				node.dirs[directory] = newTreeNode()
			}
			node = node.dirs[directory]
		}
		node.files[parts[len(parts)-1]] = blob
	}
	return root, nil
}

func encodeTreeNode(node *treeNode, objects map[string]rewrite.Object) (rewrite.Object, error) {
	entries := make([]rewrite.TreeEntry, 0, len(node.files)+len(node.dirs))
	fileNames := make([]string, 0, len(node.files))
	for name := range node.files {
		fileNames = append(fileNames, name)
	}
	sort.Strings(fileNames)
	for _, name := range fileNames {
		blob := node.files[name]
		objects[blob.OID] = blob
		entries = append(entries, rewrite.TreeEntry{Mode: "100644", Name: name, OID: blob.OID})
	}
	dirNames := make([]string, 0, len(node.dirs))
	for name := range node.dirs {
		dirNames = append(dirNames, name)
	}
	sort.Strings(dirNames)
	for _, name := range dirNames {
		tree, err := encodeTreeNode(node.dirs[name], objects)
		if err != nil {
			return rewrite.Object{}, err
		}
		objects[tree.OID] = tree
		entries = append(entries, rewrite.TreeEntry{Mode: "40000", Name: name, OID: tree.OID})
	}
	return rewrite.EncodeTree(entries)
}

func writeObjectDelta(path, branch, head string, objects map[string]rewrite.Object, manifest map[string]any) error {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(output)
	manifestJSON, err := json.Marshal(manifest)
	plain := map[string][]byte{
		"mwp.json":                  manifestJSON,
		".git/HEAD":                 []byte("ref: refs/heads/" + branch + "\n"),
		".git/refs/heads/" + branch: []byte(head + "\n"),
		".git/config":               []byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n"),
	}
	for name, data := range plain {
		if err != nil {
			break
		}
		entry, createErr := writer.Create(name)
		if createErr == nil {
			_, createErr = entry.Write(data)
		}
		err = createErr
	}
	oids := make([]string, 0, len(objects))
	for oid := range objects {
		oids = append(oids, oid)
	}
	sort.Strings(oids)
	for _, oid := range oids {
		if err != nil {
			break
		}
		header := &zip.FileHeader{Name: ".git/objects/" + oid[:2] + "/" + oid[2:], Method: zip.Store}
		entry, createErr := writer.CreateHeader(header)
		if createErr == nil {
			_, createErr = entry.Write(objects[oid].Compressed)
		}
		err = createErr
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

func CreateMergeArchive(targetPath, targetHead, sourcePath, sourceHead, treePath, outputPath, projectID, remixParent, baseCommit, branch, author, message string, timestamp int64) restoreResult {
	targetReader, target, err := openArchive(targetPath)
	if err != nil {
		return restoreResult{Error: "target workspace archive is unavailable"}
	}
	defer targetReader.Close()
	sourceReader, source, err := openArchive(sourcePath)
	if err != nil {
		return restoreResult{Error: "source workspace archive is unavailable"}
	}
	defer sourceReader.Close()
	if _, ok := target.parseCommit(targetHead); !ok {
		return restoreResult{Error: "target head is unavailable"}
	}
	if _, ok := source.parseCommit(sourceHead); !ok {
		return restoreResult{Error: "source head is unavailable"}
	}
	root, err := readMergedTree(treePath)
	if err != nil {
		return restoreResult{Error: "merged project tree is invalid"}
	}
	objects := map[string]rewrite.Object{}
	for objectPath, item := range source.objects {
		parts := strings.Split(objectPath, "/")
		if len(parts) != 4 || len(parts[2])+len(parts[3]) != 40 {
			continue
		}
		file, openErr := item.Open()
		if openErr != nil {
			return restoreResult{Error: "source Git object is unavailable"}
		}
		compressed, readErr := io.ReadAll(file)
		_ = file.Close()
		if readErr != nil {
			return restoreResult{Error: "source Git object is unavailable"}
		}
		oid := parts[2] + parts[3]
		objects[oid] = rewrite.Object{OID: oid, Compressed: compressed}
	}
	tree, err := encodeTreeNode(root, objects)
	if err != nil {
		return restoreResult{Error: "could not encode merged project tree"}
	}
	objects[tree.OID] = tree
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}
	if message == "" {
		message = "Merge pull request"
	}
	commit, err := rewrite.EncodeCommit(rewrite.CommitInput{Tree: tree.OID, Parents: []string{targetHead, sourceHead}, Author: author, Timestamp: timestamp, Message: message})
	if err != nil {
		return restoreResult{Error: "could not create merge commit"}
	}
	objects[commit.OID] = commit
	date := timestamp * 1000
	commitMetadata := map[string]any{"sha": commit.OID, "message": message, "author": author, "date": date}
	node := map[string]any{"sha": commit.OID, "message": message, "author": author, "date": date, "parents": []string{targetHead, sourceHead}, "branches": []string{branch}}
	manifest := map[string]any{
		"format": "mistwarp-project", "version": 1, "createdWith": "MistWarp API", "projectId": projectID,
		"remixParent": nil, "baseCommit": nil, "branch": branch, "head": commit.OID, "worktree": false,
		"baseHead": targetHead, "delta": true, "commits": []any{commitMetadata},
		"graph": map[string]any{"branches": []string{branch}, "branchLogs": []any{map[string]any{"branch": branch, "oids": []string{commit.OID, targetHead}}}, "nodes": []any{node}},
	}
	if remixParent != "" {
		manifest["remixParent"] = remixParent
	}
	if baseCommit != "" {
		manifest["baseCommit"] = baseCommit
	}
	if err := writeObjectDelta(outputPath, branch, commit.OID, objects, manifest); err != nil {
		return restoreResult{Error: "could not write merge history"}
	}
	return restoreResult{OK: true, Head: commit.OID, Manifest: manifest}
}

func CreateMergeArchiveJSON(targetPath, targetHead, sourcePath, sourceHead, treePath, outputPath, projectID, remixParent, baseCommit, branch, author, message string, timestamp int64) string {
	result := CreateMergeArchive(targetPath, targetHead, sourcePath, sourceHead, treePath, outputPath, projectID, remixParent, baseCommit, branch, author, message, timestamp)
	encoded, _ := json.Marshal(result)
	return string(encoded)
}
