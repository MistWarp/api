package gitinspection

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRestoreArchiveCreatesDeltaCommit(t *testing.T) {
	repository := newTestRepository()
	oldBlob := repository.object("blob", []byte("old\n"))
	oldTree := repository.tree("main.fractch", oldBlob)
	oldHead := repository.commit(oldTree, "", "Old")
	newBlob := repository.object("blob", []byte("new\n"))
	newTree := repository.tree("main.fractch", newBlob)
	currentHead := repository.commit(newTree, oldHead, "New")
	workspace := repository.write(t)
	output := filepath.Join(t.TempDir(), "restore.mwp")

	result := RestoreArchive(
		workspace, currentHead, oldHead, output, "project-1", "", "", "main", "Mist", "Restore old", 1724977000,
	)
	if !result.OK || result.Head == "" {
		t.Fatalf("restore failed: %#v", result)
	}
	if result.Manifest["delta"] != true || result.Manifest["baseHead"] != currentHead {
		t.Fatalf("unexpected manifest: %#v", result.Manifest)
	}
	reader, delta, err := openArchive(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	commit, ok := delta.parseCommit(result.Head)
	if !ok {
		t.Fatal("restore commit was not written")
	}
	if commit.Tree != oldTree || len(commit.Parents) != 1 || commit.Parents[0] != currentHead {
		t.Fatalf("unexpected restore commit: %#v", commit)
	}

	encoded := RestoreArchiveJSON(workspace, currentHead, oldHead, filepath.Join(t.TempDir(), "json.mwp"), "project-1", "", "", "main", "Mist", "", 1724977000)
	var response map[string]any
	if err := json.Unmarshal([]byte(encoded), &response); err != nil || response["ok"] != true {
		t.Fatalf("unexpected JSON response: %s", encoded)
	}
}

func TestRestoreArchiveRejectsUnreachableCommit(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("main\n"))
	tree := repository.tree("main.fractch", blob)
	currentHead := repository.commit(tree, "", "Current")
	unrelated := repository.commit(tree, "", "Unrelated")
	result := RestoreArchive(repository.write(t), currentHead, unrelated, filepath.Join(t.TempDir(), "restore.mwp"), "project-1", "", "", "main", "Mist", "", 1)
	if result.OK || result.Error == "" {
		t.Fatalf("unreachable restore unexpectedly succeeded: %#v", result)
	}
}
