package gitmanagement

import (
	"archive/zip"
	"compress/zlib"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func object(t *testing.T, kind string, body []byte) (string, []byte) {
	t.Helper()
	oid, compressed, err := writeObject(kind, body)
	if err != nil {
		t.Fatal(err)
	}
	return oid, compressed
}

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	tree, treeData := object(t, "tree", nil)
	baseBody := []byte("tree " + tree + "\nauthor Mist <mist@example.com> 100 +0000\ncommitter Mist <mist@example.com> 100 +0000\n\nInitial\n")
	base, baseData := object(t, "commit", baseBody)
	headBody := []byte("tree " + tree + "\nparent " + base + "\nauthor Mist <mist@example.com> 200 +0000\ncommitter Mist <mist@example.com> 200 +0000\n\nOld message\n")
	head, headData := object(t, "commit", headBody)
	path := filepath.Join(t.TempDir(), "fixture.mwp")
	file, _ := os.Create(path)
	writer := zip.NewWriter(file)
	for oid, data := range map[string][]byte{tree: treeData, base: baseData, head: headData} {
		entry, _ := writer.Create(".git/objects/" + oid[:2] + "/" + oid[2:])
		entry.Write(data)
	}
	ref, _ := writer.Create(".git/refs/heads/main")
	ref.Write([]byte(head + "\n"))
	manifest, _ := writer.Create("mwp.json")
	manifest.Write([]byte(`{"branch":"main","head":"` + head + `"}`))
	writer.Close()
	file.Close()
	return path, base, head
}

func TestRewriteFirstParent(t *testing.T) {
	input, base, head := fixture(t)
	output := filepath.Join(t.TempDir(), "rewritten.mwp")
	var response result
	if err := json.Unmarshal([]byte(RewriteFirstParent(input, output, base, head, "main", "Renamed")), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Head == head || response.Rewritten[base] == base {
		t.Fatalf("unexpected response: %#v", response)
	}
	reader, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	archive := archive{objects: map[string]*zip.File{}}
	for _, item := range reader.File {
		if len(item.Name) > len(".git/objects/") && item.Name[:len(".git/objects/")] == ".git/objects/" {
			archive.objects[item.Name] = item
		}
	}
	_, renamedBody, ok := archive.readObject(response.Rewritten[base])
	if !ok {
		t.Fatal("renamed commit missing")
	}
	_, renamedMessage, _ := commitParts(renamedBody)
	if renamedMessage != "Renamed\n" {
		t.Fatalf("message = %q", renamedMessage)
	}
	_, descendantBody, ok := archive.readObject(response.Head)
	if !ok {
		t.Fatal("rewritten descendant missing")
	}
	headers, descendantMessage, _ := commitParts(descendantBody)
	if parents(headers)[0] != response.Rewritten[base] || descendantMessage != "Old message\n" {
		t.Fatalf("descendant was not preserved: %v %q", headers, descendantMessage)
	}
	// The stored object remains a valid zlib stream.
	for _, item := range reader.File {
		if item.Name == ".git/objects/"+response.Head[:2]+"/"+response.Head[2:] {
			source, _ := item.Open()
			inflater, err := zlib.NewReader(source)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, inflater)
			inflater.Close()
			source.Close()
		}
	}
}

func TestRejectsCommitOutsideFirstParent(t *testing.T) {
	input, _, head := fixture(t)
	response := RewriteFirstParent(input, filepath.Join(t.TempDir(), "out.mwp"), "a000000000000000000000000000000000000000", head, "main", "No")
	var parsed result
	json.Unmarshal([]byte(response), &parsed)
	if parsed.OK || parsed.Error == "" {
		t.Fatalf("expected failure: %s", response)
	}
}

func TestRejectsUnsafeBranch(t *testing.T) {
	input, base, head := fixture(t)
	response := RewriteFirstParent(input, filepath.Join(t.TempDir(), "out.mwp"), base, head, "../escape", "No")
	var parsed result
	json.Unmarshal([]byte(response), &parsed)
	if parsed.OK || parsed.Error != "branch name is invalid" {
		t.Fatalf("expected invalid branch failure: %s", response)
	}
}
