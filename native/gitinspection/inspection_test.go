package gitinspection

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

type testRepository struct {
	objects map[string][]byte
}

func newTestRepository() *testRepository {
	return &testRepository{objects: make(map[string][]byte)}
}

func (r *testRepository) object(kind string, body []byte) string {
	raw := append([]byte(kind+" "+strconv.Itoa(len(body))+"\x00"), body...)
	digest := sha1.Sum(raw)
	oid := hex.EncodeToString(digest[:])
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, _ = writer.Write(raw)
	_ = writer.Close()
	r.objects[oid] = compressed.Bytes()
	return oid
}

func (r *testRepository) tree(path, oid string) string {
	decoded, _ := hex.DecodeString(oid)
	body := append([]byte("100644 "+path+"\x00"), decoded...)
	return r.object("tree", body)
}

func (r *testRepository) commit(tree, parent, message string) string {
	body := "tree " + tree + "\n"
	if parent != "" {
		body += "parent " + parent + "\n"
	}
	body += "author Mist <mist@mistwarp.local> 1724976000 +0000\n"
	body += "committer Mist <mist@mistwarp.local> 1724976000 +0000\n\n" + message + "\n"
	return r.object("commit", []byte(body))
}

func (r *testRepository) write(t *testing.T) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "workspace-*.mwp")
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for oid, data := range r.objects {
		entry, err := writer.Create(".git/objects/" + oid[:2] + "/" + oid[2:])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Name()
}

func decodeResult(t *testing.T, value string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestInspectCommitReportsModifiedFile(t *testing.T) {
	repository := newTestRepository()
	oldBlob := repository.object("blob", []byte("old\n"))
	oldTree := repository.tree("main.fractch", oldBlob)
	parent := repository.commit(oldTree, "", "Initial version")
	newBlob := repository.object("blob", []byte("new\n"))
	newTree := repository.tree("main.fractch", newBlob)
	head := repository.commit(newTree, parent, "Change code")

	result := decodeResult(t, Inspect(repository.write(t), head, "commit", "", head))
	if result["ok"] != true || result["parent"] != parent || result["tree"] != newTree || result["parentTree"] != oldTree {
		t.Fatalf("unexpected result: %#v", result)
	}
	files := result["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["status"] != "modified" {
		t.Fatalf("expected one modified file, got %#v", files)
	}
}

func TestInspectInitialCommitReportsAddedFile(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("code\n"))
	tree := repository.tree("main.fractch", blob)
	head := repository.commit(tree, "", "Initial version")

	result := decodeResult(t, Inspect(repository.write(t), head, "commit", "", head))
	if result["parentTree"] != "" {
		t.Fatalf("initial commit should have no parent tree, got %#v", result["parentTree"])
	}
	files := result["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["status"] != "added" {
		t.Fatalf("expected one added file, got %#v", files)
	}
}

func TestInspectTreeAndFile(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("code\n"))
	tree := repository.tree("main.fractch", blob)
	head := repository.commit(tree, "", "Initial version")
	path := repository.write(t)

	treeResult := decodeResult(t, Inspect(path, head, "tree", "", head))
	if len(treeResult["files"].([]any)) != 1 {
		t.Fatalf("unexpected tree: %#v", treeResult)
	}
	fileResult := decodeResult(t, Inspect(path, head, "file", "main.fractch", head))
	if fileResult["content"] != "Y29kZQo=" {
		t.Fatalf("unexpected file: %#v", fileResult)
	}
}

func TestInspectRejectsUnreachableCommit(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("code\n"))
	tree := repository.tree("main.fractch", blob)
	head := repository.commit(tree, "", "Initial version")
	other := repository.commit(tree, "", "Other")

	result := decodeResult(t, Inspect(repository.write(t), other, "commit", "", head))
	if result["ok"] != false || result["error"] != "commit is not reachable from this project's history" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestCompareArchivesReportsChangesWithoutCombiningRepositories(t *testing.T) {
	baseRepository := newTestRepository()
	oldBlob := baseRepository.object("blob", []byte("old\n"))
	oldTree := baseRepository.tree("main.fractch", oldBlob)
	base := baseRepository.commit(oldTree, "", "Initial version")

	headRepository := newTestRepository()
	newBlob := headRepository.object("blob", []byte("new\n"))
	newTree := headRepository.tree("main.fractch", newBlob)
	head := headRepository.commit(newTree, base, "Change remix")

	result := decodeResult(t, CompareArchives(
		baseRepository.write(t), base, headRepository.write(t), head,
	))
	if result["ok"] != true || result["parent"] != base || result["sha"] != head {
		t.Fatalf("unexpected result: %#v", result)
	}
	files := result["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["status"] != "modified" {
		t.Fatalf("expected one modified file, got %#v", files)
	}
}

func TestCompareArchivesSupportsIndependentForkHistories(t *testing.T) {
	baseRepository := newTestRepository()
	baseBlob := baseRepository.object("blob", []byte("base\n"))
	baseTree := baseRepository.tree("main.fractch", baseBlob)
	base := baseRepository.commit(baseTree, "", "Base")

	headRepository := newTestRepository()
	headBlob := headRepository.object("blob", []byte("head\n"))
	headTree := headRepository.tree("main.fractch", headBlob)
	head := headRepository.commit(headTree, "", "Unrelated")

	result := decodeResult(t, CompareArchives(
		baseRepository.write(t), base, headRepository.write(t), head,
	))
	if result["ok"] != true || result["parent"] != base || result["sha"] != head {
		t.Fatalf("unexpected result: %#v", result)
	}
	files := result["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["status"] != "modified" {
		t.Fatalf("expected the independent fork tree to be compared, got %#v", files)
	}
}
