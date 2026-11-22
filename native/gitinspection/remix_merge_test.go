package gitinspection

import (
	"strings"
	"testing"
)

func (r *testRepository) mergeCommit(tree, message string, parents ...string) string {
	body := "tree " + tree + "\n"
	for _, parent := range parents {
		body += "parent " + parent + "\n"
	}
	body += "author Mist <mist@mistwarp.local> 1724976000 +0000\n"
	body += "committer Mist <mist@mistwarp.local> 1724976000 +0000\n\n" + message + "\n"
	return r.object("commit", []byte(body))
}

func (r *testRepository) copyFrom(other *testRepository) {
	for oid, object := range other.objects {
		r.objects[oid] = object
	}
}

func (r *testRepository) fileCommit(content, parent, message string) string {
	blob := r.object("blob", []byte(content))
	return r.commit(r.tree("main.fractch", blob), parent, message)
}

func TestPrepareRemixMergeBringsOriginalChangesIntoRemix(t *testing.T) {
	original := newTestRepository()
	base := original.fileCommit("one\ntwo\nthree\n", "", "Base")
	upstream := original.fileCommit("one\ntwo\nTHREE\n", base, "Upstream")

	remix := newTestRepository()
	remix.copyFrom(original)
	delete(remix.objects, upstream)
	own := remix.fileCommit("ONE\ntwo\nthree\n", base, "Remix")

	result := PrepareRemixMerge(remix.write(t), own, original.write(t), upstream, "this remix", "original")
	if !result.OK || result.BaseHead != base || result.AlreadyMerged || result.FastForward || len(result.Conflicts) != 0 || len(result.Changes) != 1 {
		t.Fatalf("unexpected remix merge result: %#v", result)
	}
	content := string(result.Changes[0].Content)
	if !strings.Contains(content, "ONE") || !strings.Contains(content, "THREE") {
		t.Fatalf("both edits were not merged: %q", content)
	}
}

func TestPrepareRemixMergeMarksFastForward(t *testing.T) {
	original := newTestRepository()
	base := original.fileCommit("one\n", "", "Base")
	remix := newTestRepository()
	remix.copyFrom(original)
	upstream := original.fileCommit("two\n", base, "Upstream")

	result := PrepareRemixMerge(remix.write(t), base, original.write(t), upstream, "this remix", "original")
	if !result.OK || !result.FastForward || result.AlreadyMerged || len(result.Changes) != 1 || string(result.Changes[0].Content) != "two\n" {
		t.Fatalf("expected a fast-forward result: %#v", result)
	}
}

func TestPrepareRemixMergeDetectsAlreadyMerged(t *testing.T) {
	original := newTestRepository()
	base := original.fileCommit("one\n", "", "Base")
	upstream := original.fileCommit("two\n", base, "Upstream")
	remix := newTestRepository()
	remix.copyFrom(original)
	own := remix.fileCommit("three\n", upstream, "Remix")

	result := PrepareRemixMerge(remix.write(t), own, original.write(t), upstream, "this remix", "original")
	if !result.OK || !result.AlreadyMerged || result.BaseHead != upstream {
		t.Fatalf("expected an already-merged result: %#v", result)
	}
}

func TestPrepareRemixMergeRejectsUnrelatedHistory(t *testing.T) {
	original := newTestRepository()
	upstream := original.fileCommit("one\n", "", "Original")
	remix := newTestRepository()
	own := remix.fileCommit("one\n", "", "Unrelated")

	result := PrepareRemixMerge(remix.write(t), own, original.write(t), upstream, "this remix", "original")
	if result.OK || result.Error == "" {
		t.Fatalf("unrelated histories should not merge: %#v", result)
	}
}

func TestRepairedRemixMergesThroughReconnectedBase(t *testing.T) {
	original := newTestRepository()
	base := original.fileCommit("one\ntwo\nthree\n", "", "Initial version")
	upstream := original.fileCommit("one\ntwo\nTHREE\n", base, "fixed space bug")

	remix := newTestRepository()
	root := remix.fileCommit("one\ntwo\nthree\n", "", "Remix initial version")
	own := remix.fileCommit("ONE\ntwo\nthree\n", root, "game save data")
	remix.copyFrom(original)
	delete(remix.objects, upstream)
	ownTree := remix.tree("main.fractch", remix.object("blob", []byte("ONE\ntwo\nthree\n")))
	repaired := remix.mergeCommit(ownTree, "Reconnect recorded remix parent history", own, base)
	originalPath := original.write(t)
	remixPath := remix.write(t)

	decoded := decodeResult(t, RemixMergeBase(originalPath, upstream, remixPath, repaired))
	if decoded["ok"] != true || decoded["base"] != base {
		t.Fatalf("expected the reconnected base, got %#v", decoded)
	}
	result := PrepareRemixMerge(originalPath, upstream, remixPath, repaired, "target", "pull request")
	if !result.OK || result.BaseHead != base || len(result.Changes) != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("unexpected pull merge result: %#v", result)
	}
	content := string(result.Changes[0].Content)
	if !strings.Contains(content, "ONE") || !strings.Contains(content, "THREE") {
		t.Fatalf("the remix change was not merged: %q", content)
	}
	stale := PrepareRemixMerge(originalPath, upstream, remixPath, root, "target", "pull request")
	if stale.OK {
		t.Fatalf("the unconnected remix root should not merge: %#v", stale)
	}
}

func TestRemixMergeBaseRejectsMissingHeads(t *testing.T) {
	original := newTestRepository()
	head := original.fileCommit("one\n", "", "Base")
	path := original.write(t)
	decoded := decodeResult(t, RemixMergeBase(path, "", path, head))
	if decoded["ok"] != false {
		t.Fatalf("a missing head should fail: %#v", decoded)
	}
}

func TestPrepareRemixMergeLabelsConflicts(t *testing.T) {
	original := newTestRepository()
	base := original.fileCommit("one\nmove 10\nthree\n", "", "Base")
	upstream := original.fileCommit("one\nmove 20\nthree\n", base, "Upstream")
	remix := newTestRepository()
	remix.copyFrom(original)
	delete(remix.objects, upstream)
	own := remix.fileCommit("one\nmove 5\nthree\n", base, "Remix")

	result := PrepareRemixMerge(remix.write(t), own, original.write(t), upstream, "this remix", "original")
	if !result.OK || len(result.Conflicts) != 1 {
		t.Fatalf("expected one conflict: %#v", result)
	}
	content := string(result.Changes[0].Content)
	if !strings.Contains(content, "<<<<<<< this remix") || !strings.Contains(content, ">>>>>>> original") {
		t.Fatalf("conflict markers are not labelled: %q", content)
	}
}

func TestCrossMergeBaseBreaksTiesTheSameWayEveryTime(t *testing.T) {
	repo := newTestRepository()
	root := repo.fileCommit("root\n", "", "Root")
	left := repo.fileCommit("left\n", root, "Left")
	right := repo.fileCommit("right\n", root, "Right")
	leftTree := repo.tree("main.fractch", repo.object("blob", []byte("left merge\n")))
	rightTree := repo.tree("main.fractch", repo.object("blob", []byte("right merge\n")))
	targetHead := repo.mergeCommit(leftTree, "Target merge", left, right)
	sourceHead := repo.mergeCommit(rightTree, "Source merge", right, left)
	path := repo.write(t)
	reader, archive, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	want := left
	if right < left {
		want = right
	}
	for attempt := 0; attempt < 50; attempt++ {
		base, ok := crossMergeBase(archive, targetHead, archive, sourceHead)
		if !ok || base != want {
			t.Fatalf("attempt %d chose %s, want %s", attempt, base, want)
		}
	}
}

func TestRemixMergeBaseIsRememberedForTheSameHeads(t *testing.T) {
	original := newTestRepository()
	base := original.fileCommit("one\n", "", "Base")
	upstream := original.fileCommit("two\n", base, "Upstream")
	remix := newTestRepository()
	remix.copyFrom(original)
	delete(remix.objects, upstream)
	own := remix.fileCommit("mine\n", base, "Remix")

	remixPath := remix.write(t)
	originalPath := original.write(t)
	first := RemixMergeBase(remixPath, own, originalPath, upstream)
	if !strings.Contains(first, base) {
		t.Fatalf("unexpected merge base: %s", first)
	}
	if cached, ok := cachedMergeBase(own + ":" + upstream); !ok || cached != base {
		t.Fatalf("merge base was not remembered: %q %v", cached, ok)
	}
	if again := RemixMergeBase(remixPath, own, originalPath, upstream); again != first {
		t.Fatalf("cached lookup changed the answer: %s", again)
	}
	missingHead := strings.Repeat("e", 40)
	rememberMergeBase(own+":"+missingHead, strings.Repeat("f", 40))
	if stale := RemixMergeBase(remixPath, own, originalPath, missingHead); strings.Contains(stale, `"ok":true`) {
		t.Fatalf("a cached base missing from the archives was trusted: %s", stale)
	}
	mergeBaseCacheMu.Lock()
	mergeBaseCache[own+":"+upstream] = strings.Repeat("f", 40)
	mergeBaseCacheMu.Unlock()
	if healed := RemixMergeBase(remixPath, own, originalPath, upstream); !strings.Contains(healed, base) {
		t.Fatalf("a stale cached base changed the answer: %s", healed)
	}
	if cached, _ := cachedMergeBase(own + ":" + upstream); cached != base {
		t.Fatalf("a stale cached base was not replaced: %q", cached)
	}
}
