package gitinspection

import (
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"testing"
)

func resetArchiveCacheForTest() {
	sharedArchives.Lock()
	defer sharedArchives.Unlock()
	for _, entry := range sharedArchives.entries {
		dropSharedArchiveLocked(entry)
	}
}

func nestedRepository(t *testing.T) (string, string) {
	repository := newTestRepository()
	files := map[string]string{
		"project.json":             repository.object("blob", []byte("{}\n")),
		"sprites/cat/code.fractch": repository.object("blob", []byte("say \"meow\"\n")),
		"sprites/cat/costume.png":  repository.object("blob", []byte("\x89PNG\x00data")),
		"sprites/dog/code.fractch": repository.object("blob", []byte("say \"woof\"\n")),
	}
	first := repository.commit(repository.nestedTree(files), "", "First")
	files["sprites/cat/code.fractch"] = repository.object("blob", []byte("say \"purr\"\n"))
	head := repository.commit(repository.nestedTree(files), first, "Second")
	return repository.write(t), head
}

func TestInspectFileFindsNestedPaths(t *testing.T) {
	resetArchiveCacheForTest()
	path, head := nestedRepository(t)
	result := decodeResult(t, Inspect(path, head, "file", "sprites/cat/code.fractch", head))
	if result["content"] != base64.StdEncoding.EncodeToString([]byte("say \"purr\"\n")) {
		t.Fatalf("unexpected nested file: %#v", result)
	}
	file := result["file"].(map[string]any)
	if file["path"] != "sprites/cat/code.fractch" || file["size"] != float64(11) || file["binary"] != false || file["mode"] != "100644" {
		t.Fatalf("unexpected file metadata: %#v", file)
	}
	binary := decodeResult(t, Inspect(path, head, "file", "sprites/cat/costume.png", head))
	if binary["file"].(map[string]any)["binary"] != true {
		t.Fatalf("binary file not flagged: %#v", binary)
	}
}

func TestInspectFileMatchesTreeListing(t *testing.T) {
	resetArchiveCacheForTest()
	path, head := nestedRepository(t)
	tree := decodeResult(t, Inspect(path, head, "tree", "", head))
	for _, item := range tree["files"].([]any) {
		listed := item.(map[string]any)
		result := decodeResult(t, Inspect(path, head, "file", listed["path"].(string), head))
		file := result["file"].(map[string]any)
		for _, key := range []string{"path", "oid", "mode", "size", "binary"} {
			if file[key] != listed[key] {
				t.Fatalf("file %s differs on %s: %#v vs %#v", listed["path"], key, file, listed)
			}
		}
	}
}

func TestInspectFileRejectsDirectoriesAndMissingPaths(t *testing.T) {
	resetArchiveCacheForTest()
	path, head := nestedRepository(t)
	for _, requested := range []string{"sprites", "sprites/cat", "sprites/cow/code.fractch", "project.json/x", "missing.txt"} {
		result := decodeResult(t, Inspect(path, head, "file", requested, head))
		if result["ok"] != false || result["error"] != "file was not found at this commit" {
			t.Fatalf("%s: expected not found, got %#v", requested, result)
		}
	}
}

func TestInspectFileReportsMissingBlob(t *testing.T) {
	resetArchiveCacheForTest()
	repository := newTestRepository()
	blob := repository.object("blob", []byte("code\n"))
	head := repository.commit(repository.tree("main.fractch", blob), "", "Initial")
	delete(repository.objects, blob)
	path := repository.write(t)
	result := decodeResult(t, Inspect(path, head, "file", "main.fractch", head))
	if result["error"] != "file object is unavailable" {
		t.Fatalf("expected missing blob, got %#v", result)
	}
}

func TestInspectSeesReplacedArchive(t *testing.T) {
	resetArchiveCacheForTest()
	path, head := nestedRepository(t)
	if decodeResult(t, Inspect(path, head, "file", "project.json", head))["ok"] != true {
		t.Fatal("first archive unreadable")
	}
	repository := newTestRepository()
	blob := repository.object("blob", []byte("replaced\n"))
	replacedHead := repository.commit(repository.tree("project.json", blob), "", "Replaced")
	replacement := repository.write(t)
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	result := decodeResult(t, Inspect(path, replacedHead, "file", "project.json", replacedHead))
	if result["content"] != base64.StdEncoding.EncodeToString([]byte("replaced\n")) {
		t.Fatalf("stale archive served: %#v", result)
	}
	if decodeResult(t, Inspect(path, head, "file", "project.json", head))["ok"] != false {
		t.Fatal("old commit still readable after the archive was replaced")
	}
}

func TestInspectReportsRemovedArchive(t *testing.T) {
	resetArchiveCacheForTest()
	path, head := nestedRepository(t)
	Inspect(path, head, "tree", "", head)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if result := decodeResult(t, Inspect(path, head, "tree", "", head)); result["error"] != "workspace archive is unavailable" {
		t.Fatalf("expected unavailable archive, got %#v", result)
	}
	sharedArchives.Lock()
	_, kept := sharedArchives.entries[path]
	sharedArchives.Unlock()
	if kept {
		t.Fatal("removed archive is still held open")
	}
}

func TestReachabilityWorksWhenMemoIsFull(t *testing.T) {
	repository := newTestRepository()
	tree := repository.tree("a.txt", repository.object("blob", []byte("a\n")))
	first := repository.commit(tree, "", "First")
	second := repository.commit(tree, first, "Second")
	path := repository.write(t)
	reader, memoized, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	memoized.memo = newObjectMemo()
	for i := 0; i < memoReachLimit; i++ {
		memoized.reachable(first, strings.Repeat(string(rune('a'+i)), 40))
	}
	if !memoized.reachable(first, second) || memoized.reachable(second, first) {
		t.Fatal("reachability is wrong once the memo is full")
	}
}

func TestInspectConcurrentRequestsAgree(t *testing.T) {
	resetArchiveCacheForTest()
	path, head := nestedRepository(t)
	want := map[string]string{}
	for _, requested := range []string{"project.json", "sprites/cat/code.fractch", "sprites/dog/code.fractch"} {
		want[requested] = Inspect(path, head, "file", requested, head)
	}
	wantTree := Inspect(path, head, "tree", "", head)
	resetArchiveCacheForTest()
	var group sync.WaitGroup
	for i := 0; i < 64; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			if i%4 == 0 {
				if got := Inspect(path, head, "tree", "", head); got != wantTree {
					t.Errorf("tree differs under concurrency")
				}
				return
			}
			for requested, expected := range want {
				if got := Inspect(path, head, "file", requested, head); got != expected {
					t.Errorf("%s differs under concurrency", requested)
				}
			}
		}(i)
	}
	group.Wait()
}

func TestSharedArchivesStayBounded(t *testing.T) {
	resetArchiveCacheForTest()
	for i := 0; i < sharedArchiveLimit+3; i++ {
		path, head := nestedRepository(t)
		Inspect(path, head, "tree", "", head)
	}
	sharedArchives.Lock()
	count := len(sharedArchives.entries)
	for _, entry := range sharedArchives.entries {
		if entry.refs != 0 {
			t.Errorf("archive %s still referenced", entry.path)
		}
	}
	sharedArchives.Unlock()
	if count > sharedArchiveLimit {
		t.Fatalf("%d archives kept open, limit %d", count, sharedArchiveLimit)
	}
}

func TestMemoReachabilityMatchesWalk(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("a\n"))
	tree := repository.tree("a.txt", blob)
	first := repository.commit(tree, "", "First")
	second := repository.commit(tree, first, "Second")
	side := repository.commit(tree, "", "Side")
	path := repository.write(t)
	reader, plain, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	memoized := plain
	memoized.memo = newObjectMemo()
	for _, sha := range []string{first, second, side, strings.Repeat("0", 40)} {
		if plain.reachable(sha, second) != memoized.reachable(sha, second) {
			t.Fatalf("reachability of %s differs with the memo", sha)
		}
	}
	if !memoized.reachable(first, second) || memoized.reachable(side, second) {
		t.Fatal("memoized reachability is wrong")
	}
}

func TestInspectionError(t *testing.T) {
	cases := map[string]string{
		`{"ok":true,"content":"eA=="}`:                  "",
		`{"error":"commit tree is invalid","ok":false}`: "commit tree is invalid",
		`{"ok":false}`: "commit not found",
		`not json`:     "could not inspect project history",
	}
	for input, want := range cases {
		if got := InspectionError(input); got != want {
			t.Fatalf("InspectionError(%q) = %q, want %q", input, got, want)
		}
	}
}

func plainInspect(t *testing.T, path, sha, operation, requestedPath, allowedHeads string) string {
	t.Helper()
	reader, plain, err := openArchive(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	commit, ok := plain.parseCommit(sha)
	if !ok || !plain.reachable(sha, allowedHeads) {
		t.Fatalf("plain archive cannot read %s", sha)
	}
	switch operation {
	case "tree":
		files, ok := plain.walkTree(commit.Tree)
		if !ok {
			t.Fatal("plain tree invalid")
		}
		return encode(map[string]any{"ok": true, "sha": sha, "commit": commit, "files": files}, "")
	case "commit-inline":
		return plain.inspectCommit(sha, commit, true)
	case "changes":
		return plain.inspectCommitChanges(sha, commit)
	case "summary":
		return plain.inspectCommitSummary(sha, commit)
	case "last-commits":
		return plain.inspectLastCommits(sha, commit)
	}
	return plain.inspectCommit(sha, commit, false)
}

func TestSharedArchiveMatchesPlainArchive(t *testing.T) {
	resetArchiveCacheForTest()
	repository := newTestRepository()
	files := map[string]string{
		"project.json":             repository.object("blob", []byte("{}\n")),
		"sprites/cat/code.fractch": repository.object("blob", []byte("sprite \"Cat\";\nwhen flag {\n  say \"a\";\n}\n")),
		"sprites/dog/code.fractch": repository.object("blob", []byte("sprite \"Dog\";\n")),
		"notes.txt":                repository.object("blob", []byte("one\n")),
	}
	heads := []string{}
	head := ""
	for i := 0; i < 12; i++ {
		switch i % 3 {
		case 0:
			files["notes.txt"] = repository.object("blob", []byte(strings.Repeat("line\n", i+1)))
		case 1:
			files["sprites/cat/code.fractch"] = repository.object("blob", []byte("sprite \"Cat\";\nwhen flag {\n  say \""+strings.Repeat("b", i)+"\";\n}\n"))
		default:
			files["sprites/new"+string(rune('a'+i))+"/code.fractch"] = repository.object("blob", []byte("sprite \"N\";\n"))
		}
		head = repository.commit(repository.nestedTree(files), head, "Save "+string(rune('a'+i)))
		heads = append(heads, head)
	}
	path := repository.write(t)
	for _, sha := range heads {
		for _, operation := range []string{"tree", "commit", "commit-inline", "changes", "summary", "last-commits"} {
			shared := Inspect(path, sha, operation, "", head)
			if plain := plainInspect(t, path, sha, operation, "", head); shared != plain {
				t.Fatalf("%s of %s differs:\nshared %s\nplain  %s", operation, sha, shared, plain)
			}
		}
	}
}
