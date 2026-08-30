package rewrite

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func rawObject(t *testing.T, object Object) (string, []byte) {
	t.Helper()
	reader, err := zlib.NewReader(bytes.NewReader(object.Compressed))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	separator := bytes.IndexByte(data, 0)
	if separator < 0 {
		t.Fatal("object has no header terminator")
	}
	digest := sha1.Sum(data)
	if hex.EncodeToString(digest[:]) != object.OID {
		t.Fatal("object ID does not match encoded bytes")
	}
	return string(data[:separator]), data[separator+1:]
}

func TestEncodeTreeAndCommit(t *testing.T) {
	blob, err := EncodeObject("blob", []byte("code\n"))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := EncodeTree([]TreeEntry{{Mode: "100644", Name: "main.fractch", OID: blob.OID}})
	if err != nil {
		t.Fatal(err)
	}
	commit, err := EncodeCommit(CommitInput{Tree: tree.OID, Author: "Mist User", Timestamp: 1724976000, Message: "Initial version"})
	if err != nil {
		t.Fatal(err)
	}
	header, body := rawObject(t, commit)
	if !strings.HasPrefix(header, "commit ") || !bytes.Contains(body, []byte("tree "+tree.OID+"\n")) {
		t.Fatalf("unexpected commit object: %q %q", header, body)
	}
	if !bytes.Contains(body, []byte("MistWarp <Mist-User@mistwarp.local> 1724976000 +0000")) {
		t.Fatalf("commit identity changed: %q", body)
	}
}

func TestEncodeTreeUsesGitOrdering(t *testing.T) {
	blob, _ := EncodeObject("blob", nil)
	tree, err := EncodeTree([]TreeEntry{
		{Mode: "100644", Name: "z", OID: blob.OID},
		{Mode: "100644", Name: "a", OID: blob.OID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, body := rawObject(t, tree)
	if !bytes.HasPrefix(body, []byte("100644 a\x00")) {
		t.Fatalf("tree entries are not sorted: %q", body)
	}
}

func TestRewriteCommitParentsPreservesOtherContent(t *testing.T) {
	tree := strings.Repeat("a", 40)
	oldParent := strings.Repeat("b", 40)
	newParent := strings.Repeat("c", 40)
	body := []byte("tree " + tree + "\nparent " + oldParent + "\nauthor Name <n@example.com> 1 +0000\ncommitter Name <n@example.com> 1 +0000\nencoding UTF-8\n\nmessage\nbody\n")
	rewritten, err := RewriteCommitParents(body, []string{newParent})
	if err != nil {
		t.Fatal(err)
	}
	_, got := rawObject(t, rewritten)
	if bytes.Contains(got, []byte(oldParent)) || !bytes.Contains(got, []byte("parent "+newParent)) {
		t.Fatalf("parents were not replaced: %q", got)
	}
	if !bytes.Contains(got, []byte("encoding UTF-8\n\nmessage\nbody\n")) {
		t.Fatalf("commit content was not preserved: %q", got)
	}
}

func TestWriteGeneratedHistoryArchive(t *testing.T) {
	directory := t.TempDir()
	projectPath := filepath.Join(directory, "project.json.gz")
	projectFile, err := os.Create(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(projectFile)
	_, _ = gzipWriter.Write([]byte(`{"targets":[]}`))
	_ = gzipWriter.Close()
	_ = projectFile.Close()
	assetsPath := filepath.Join(directory, "assets")
	if err := os.Mkdir(assetsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetsPath, "asset.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(directory, "workspace.mwp")
	head, err := WriteGeneratedHistoryArchive(GeneratedHistoryOptions{
		ProjectJSONPath: projectPath, AssetsPath: assetsPath, OutputPath: outputPath,
		ProjectID: "project-1", Owner: "Mist", CreatedAtMillis: 1724976000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(head) != 40 {
		t.Fatalf("invalid head %q", head)
	}
	archive, err := zip.OpenReader(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	entries := make(map[string]*zip.File)
	for _, entry := range archive.File {
		entries[entry.Name] = entry
	}
	for _, name := range []string{"mwp.json", ".git/HEAD", ".git/refs/heads/main", ".git/objects/" + head[:2] + "/" + head[2:]} {
		if entries[name] == nil {
			t.Fatalf("archive is missing %s", name)
		}
	}
	source, _ := entries["mwp.json"].Open()
	manifestBytes, _ := io.ReadAll(source)
	_ = source.Close()
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["head"] != head || manifest["projectId"] != "project-1" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func writeTestArchive(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, value := range entries {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, createErr = entry.Write([]byte(value)); createErr != nil {
			t.Fatal(createErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPruneDeltaArchiveRemovesInheritedObjectsOnly(t *testing.T) {
	directory := t.TempDir()
	basePath := filepath.Join(directory, "base.mwp")
	deltaPath := filepath.Join(directory, "delta.mwp")
	outputPath := filepath.Join(directory, "pruned.mwp")
	inherited := ".git/objects/aa/old"
	added := ".git/objects/bb/new"
	writeTestArchive(t, basePath, map[string]string{inherited: "old"})
	writeTestArchive(t, deltaPath, map[string]string{
		inherited: "old", added: "new", "mwp.json": "manifest",
		".git/refs/heads/main": "head\n",
	})

	result := PruneDeltaArchive(basePath, deltaPath, outputPath)
	if !result.OK || result.RemovedObjects != 1 || result.KeptObjects != 1 {
		t.Fatalf("unexpected prune result: %#v", result)
	}
	archive, err := zip.OpenReader(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	names := map[string]bool{}
	for _, entry := range archive.File {
		names[entry.Name] = true
	}
	if names[inherited] || !names[added] || !names["mwp.json"] || !names[".git/refs/heads/main"] {
		t.Fatalf("wrong pruned entries: %#v", names)
	}
}
