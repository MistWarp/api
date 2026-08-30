package gitinspection

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func workspaceWithManifest(t *testing.T, sourcePath, head, branch string) string {
	t.Helper()
	source, err := zip.OpenReader(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(t.TempDir(), "source.mwp")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, item := range source.File {
		from, _ := item.Open()
		to, _ := writer.CreateHeader(&item.FileHeader)
		_, _ = io.Copy(to, from)
		_ = from.Close()
	}
	manifest, _ := json.Marshal(map[string]any{
		"format": "mistwarp-project", "version": 1, "projectId": "source", "branch": branch, "head": head,
		"commits": []any{map[string]any{"sha": head, "message": "Change"}},
		"graph": map[string]any{
			"branches":   []any{branch},
			"branchLogs": []any{map[string]any{"branch": branch, "oids": []any{head}}},
			"nodes":      []any{map[string]any{"sha": head, "branches": []any{branch}}},
		},
	})
	entry, _ := writer.Create("mwp.json")
	_, _ = entry.Write(manifest)
	entry, _ = writer.Create(".git/HEAD")
	_, _ = entry.Write([]byte("ref: refs/heads/" + branch + "\n"))
	entry, _ = writer.Create(".git/refs/heads/" + branch)
	_, _ = entry.Write([]byte(head + "\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFastForwardArchiveMovesTargetBranch(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("change\n"))
	tree := repository.tree("main.fractch", blob)
	head := repository.commit(tree, "", "Change")
	source := workspaceWithManifest(t, repository.write(t), head, "remix-work")
	output := filepath.Join(t.TempDir(), "target.mwp")

	result := FastForwardArchive(source, head, output, "target", "", "", "main")
	if !result.OK {
		t.Fatalf("fast-forward failed: %#v", result)
	}
	if result.Manifest["branch"] != "main" || result.Manifest["head"] != head {
		t.Fatalf("target manifest did not move main: %#v", result.Manifest)
	}
	reader, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	foundRef := false
	for _, item := range reader.File {
		if item.Name != ".git/refs/heads/main" {
			continue
		}
		file, _ := item.Open()
		value, _ := io.ReadAll(file)
		_ = file.Close()
		foundRef = string(value) == head+"\n"
	}
	if !foundRef {
		t.Fatal("target main ref was not written")
	}
}

func TestFastForwardArchiveDropsSyntheticPullBranches(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("change\n"))
	tree := repository.tree("main.fractch", blob)
	head := repository.commit(tree, "", "Change")
	sourcePath := workspaceWithManifest(t, repository.write(t), head, "main")

	reader, err := zip.OpenReader(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "source-with-pull-ref.mwp")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, item := range reader.File {
		if item.Name == "mwp.json" {
			continue
		}
		from, _ := item.Open()
		to, _ := writer.CreateHeader(&item.FileHeader)
		_, _ = io.Copy(to, from)
		_ = from.Close()
	}
	_ = reader.Close()
	manifest, _ := json.Marshal(map[string]any{
		"format": "mistwarp-project", "version": 1, "projectId": "source", "branch": "main", "head": head,
		"commits": []any{map[string]any{"sha": head, "message": "Change"}},
		"graph": map[string]any{
			"branches": []any{"main", "mwp-pr-1"},
			"branchLogs": []any{
				map[string]any{"branch": "main", "oids": []any{head}},
				map[string]any{"branch": "mwp-pr-1", "oids": []any{head}},
			},
			"nodes": []any{map[string]any{"sha": head, "branches": []any{"main", "mwp-pr-1"}}},
		},
	})
	entry, _ := writer.Create("mwp.json")
	_, _ = entry.Write(manifest)
	entry, _ = writer.Create(".git/refs/heads/mwp-pr-1")
	_, _ = entry.Write([]byte(head + "\n"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "target.mwp")
	result := FastForwardArchive(path, head, output, "target", "", "", "main")
	if !result.OK {
		t.Fatalf("fast-forward failed: %#v", result)
	}
	graph := result.Manifest["graph"].(map[string]any)
	branches := graph["branches"].([]any)
	if len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("synthetic branch survived in manifest: %#v", graph)
	}
	nodes := graph["nodes"].([]any)
	nodeBranches := nodes[0].(map[string]any)["branches"].([]any)
	if len(nodeBranches) != 1 || nodeBranches[0] != "main" {
		t.Fatalf("synthetic node label survived: %#v", nodes[0])
	}
	outputReader, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer outputReader.Close()
	for _, item := range outputReader.File {
		if item.Name == ".git/refs/heads/mwp-pr-1" {
			t.Fatal("synthetic pull ref survived in archive")
		}
	}
}
