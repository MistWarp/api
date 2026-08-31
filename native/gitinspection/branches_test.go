package gitinspection

import (
	"archive/zip"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
)

func branchNames(t *testing.T, path string) ([]any, map[string]string, map[string]any) {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	refs := map[string]string{}
	for _, item := range reader.File {
		if len(item.Name) <= len(".git/refs/heads/") || item.Name[:len(".git/refs/heads/")] != ".git/refs/heads/" {
			continue
		}
		file, _ := item.Open()
		value, _ := io.ReadAll(file)
		_ = file.Close()
		refs[item.Name[len(".git/refs/heads/"):]] = string(value)
	}
	manifest := map[string]any{}
	for _, item := range reader.File {
		if item.Name != "mwp.json" {
			continue
		}
		file, _ := item.Open()
		if err := json.NewDecoder(file).Decode(&manifest); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		break
	}
	graph := manifest["graph"].(map[string]any)
	return graph["branches"].([]any), refs, manifest
}

func TestManageBranchArchiveCreatesBranchFromSelectedTip(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("project\n"))
	head := repository.commit(repository.tree("main.fractch", blob), "", "Initial")
	source := workspaceWithManifest(t, repository.write(t), head, "main")
	output := filepath.Join(t.TempDir(), "created.mwp")

	result := ManageBranchArchive(source, output, "create", "", "feature/ui", "main", "")
	if !result.OK {
		t.Fatalf("create failed: %#v", result)
	}
	branches, refs, manifest := branchNames(t, output)
	if len(branches) != 2 || branches[1] != "feature/ui" || refs["feature/ui"] != head+"\n" {
		t.Fatalf("created branch is missing: branches=%#v refs=%#v", branches, refs)
	}
	if manifest["branch"] != "main" || manifest["head"] != head {
		t.Fatalf("creating a branch changed the active branch: %#v", manifest)
	}
}

func TestManageBranchArchiveRenamesCurrentBranch(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("project\n"))
	head := repository.commit(repository.tree("main.fractch", blob), "", "Initial")
	source := workspaceWithManifest(t, repository.write(t), head, "main")
	output := filepath.Join(t.TempDir(), "renamed.mwp")

	result := ManageBranchArchive(source, output, "rename", "main", "stable", "", "")
	if !result.OK {
		t.Fatalf("rename failed: %#v", result)
	}
	branches, refs, manifest := branchNames(t, output)
	if len(branches) != 1 || branches[0] != "stable" || refs["stable"] != head+"\n" || refs["main"] != "" {
		t.Fatalf("renamed branch refs are wrong: branches=%#v refs=%#v", branches, refs)
	}
	if manifest["branch"] != "stable" {
		t.Fatalf("active branch was not renamed: %#v", manifest)
	}
}

func TestManageBranchArchiveDeletesOnlyNonCurrentBranch(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("project\n"))
	head := repository.commit(repository.tree("main.fractch", blob), "", "Initial")
	source := workspaceWithManifest(t, repository.write(t), head, "main")
	created := filepath.Join(t.TempDir(), "created.mwp")
	if result := ManageBranchArchive(source, created, "create", "", "old", "main", ""); !result.OK {
		t.Fatalf("setup failed: %#v", result)
	}
	output := filepath.Join(t.TempDir(), "deleted.mwp")
	result := ManageBranchArchive(created, output, "delete", "old", "", "", "")
	if !result.OK {
		t.Fatalf("delete failed: %#v", result)
	}
	branches, refs, _ := branchNames(t, output)
	if len(branches) != 1 || branches[0] != "main" || refs["old"] != "" {
		t.Fatalf("deleted branch survived: branches=%#v refs=%#v", branches, refs)
	}
	blocked := ManageBranchArchive(output, filepath.Join(t.TempDir(), "blocked.mwp"), "delete", "main", "", "", "")
	if blocked.OK || blocked.Error != "the current branch cannot be deleted" {
		t.Fatalf("current branch deletion was not blocked: %#v", blocked)
	}
}

func TestManageBranchArchiveRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"feature//ui", "release/", "a..b", "topic/.hidden", "topic.lock"} {
		if validBranchName(name) {
			t.Fatalf("unsafe branch name accepted: %q", name)
		}
	}
}

func TestManageBranchArchiveUsesAuthoritativeProjectGraph(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("project\n"))
	head := repository.commit(repository.tree("main.fractch", blob), "", "Initial")
	source := workspaceWithManifest(t, repository.write(t), head, "main")
	history, _ := json.Marshal(map[string]any{
		"branch": "main",
		"head":   head,
		"commits": []any{
			map[string]any{"sha": head, "message": "Initial"},
		},
		"graph": map[string]any{
			"branches": []any{"main", "release"},
			"branchLogs": []any{
				map[string]any{"branch": "main", "oids": []any{head}},
				map[string]any{"branch": "release", "oids": []any{head}},
			},
			"nodes": []any{map[string]any{"sha": head, "branches": []any{"main", "release"}}},
		},
	})
	output := filepath.Join(t.TempDir(), "authoritative.mwp")

	result := ManageBranchArchive(source, output, "create", "", "feature", "main", string(history))
	if !result.OK {
		t.Fatalf("create failed: %#v", result)
	}
	branches, refs, _ := branchNames(t, output)
	if len(branches) != 3 || branches[1] != "release" || refs["release"] != head+"\n" {
		t.Fatalf("server-side branch history was dropped: branches=%#v refs=%#v", branches, refs)
	}
}
