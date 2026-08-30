package gitinspection

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareMergeMergesTextWithoutArchivesInTheClient(t *testing.T) {
	baseRepository := newTestRepository()
	baseBlob := baseRepository.object("blob", []byte("one\ntwo\nthree\n"))
	baseTree := baseRepository.tree("main.fractch", baseBlob)
	base := baseRepository.commit(baseTree, "", "Base")
	oursBlob := baseRepository.object("blob", []byte("ONE\ntwo\nthree\n"))
	oursTree := baseRepository.tree("main.fractch", oursBlob)
	ours := baseRepository.commit(oursTree, base, "Ours")

	sourceRepository := newTestRepository()
	for oid, object := range baseRepository.objects {
		sourceRepository.objects[oid] = object
	}
	theirsBlob := sourceRepository.object("blob", []byte("one\ntwo\nTHREE\n"))
	theirsTree := sourceRepository.tree("main.fractch", theirsBlob)
	theirs := sourceRepository.commit(theirsTree, base, "Theirs")

	result := PrepareMerge(baseRepository.write(t), ours, sourceRepository.write(t), theirs, base)
	if !result.OK || len(result.Conflicts) != 0 || len(result.Changes) != 1 {
		t.Fatalf("unexpected merge result: %#v", result)
	}
	content := string(result.Changes[0].Content)
	if !strings.Contains(content, "ONE") || !strings.Contains(content, "THREE") {
		t.Fatalf("both edits were not merged: %q", content)
	}
}

func TestPrepareBranchMergeFindsCommonAncestor(t *testing.T) {
	repository := newTestRepository()
	baseBlob := repository.object("blob", []byte("one\ntwo\nthree\n"))
	baseTree := repository.tree("main.fractch", baseBlob)
	base := repository.commit(baseTree, "", "Base")
	targetBlob := repository.object("blob", []byte("ONE\ntwo\nthree\n"))
	target := repository.commit(repository.tree("main.fractch", targetBlob), base, "Target")
	sourceBlob := repository.object("blob", []byte("one\ntwo\nTHREE\n"))
	source := repository.commit(repository.tree("main.fractch", sourceBlob), base, "Source")

	result := PrepareBranchMerge(repository.write(t), target, source)
	if !result.OK || result.BaseHead != base || result.AlreadyMerged || len(result.Changes) != 1 {
		t.Fatalf("unexpected branch merge result: %#v", result)
	}
}

func TestPrepareBranchMergeDetectsAlreadyMerged(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("project\n"))
	tree := repository.tree("main.fractch", blob)
	base := repository.commit(tree, "", "Base")
	target := repository.commit(tree, base, "Target")

	result := PrepareBranchMerge(repository.write(t), target, base)
	if !result.OK || !result.AlreadyMerged || result.BaseHead != base {
		t.Fatalf("expected an already-merged result: %#v", result)
	}
}

func writeTreeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tree.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range files {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		_, _ = entry.Write([]byte(content))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCreateMergeArchiveWritesTwoParentCommit(t *testing.T) {
	target := newTestRepository()
	baseBlob := target.object("blob", []byte("base\n"))
	baseTree := target.tree("main.fractch", baseBlob)
	base := target.commit(baseTree, "", "Base")
	ours := target.commit(baseTree, base, "Ours")
	source := newTestRepository()
	for oid, object := range target.objects {
		source.objects[oid] = object
	}
	theirs := source.commit(baseTree, base, "Theirs")
	output := filepath.Join(t.TempDir(), "merge.mwp")
	result := CreateMergeArchive(target.write(t), ours, source.write(t), theirs, writeTreeZip(t, map[string]string{"main.fractch": "merged\n"}), output, "project", "", "", "main", "Mist", "Merge", 1724977000)
	if !result.OK {
		t.Fatalf("merge commit failed: %#v", result)
	}
	reader, archive, err := openArchive(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	commit, ok := archive.parseCommit(result.Head)
	if !ok || len(commit.Parents) != 2 || commit.Parents[0] != ours || commit.Parents[1] != theirs {
		t.Fatalf("unexpected merge commit: %#v", commit)
	}
}

func TestCreateFastForwardMergeArchiveStillWritesMergeCommit(t *testing.T) {
	target := newTestRepository()
	baseBlob := target.object("blob", []byte("base\n"))
	baseTree := target.tree("main.fractch", baseBlob)
	base := target.commit(baseTree, "", "Base")

	source := newTestRepository()
	for oid, object := range target.objects {
		source.objects[oid] = object
	}
	sourceBlob := source.object("blob", []byte("pull request\n"))
	sourceTree := source.tree("main.fractch", sourceBlob)
	sourceHead := source.commit(sourceTree, base, "Pull request change")
	output := filepath.Join(t.TempDir(), "pull-merge.mwp")

	result := CreateFastForwardMergeArchive(
		target.write(t), base, source.write(t), sourceHead, output,
		"project", "", "", "main", "Mist", "Merge pull request #1", 1724977000,
	)
	if !result.OK {
		t.Fatalf("merge commit failed: %#v", result)
	}
	reader, archive, err := openArchive(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	commit, ok := archive.parseCommit(result.Head)
	if !ok || commit.Tree != sourceTree || len(commit.Parents) != 2 || commit.Parents[0] != base || commit.Parents[1] != sourceHead {
		t.Fatalf("unexpected merge commit: %#v", commit)
	}
	graph := result.Manifest["graph"].(map[string]any)
	branches := graph["branches"].([]string)
	if len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("unexpected target branches: %#v", branches)
	}
}
