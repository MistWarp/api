package gitinspection

import (
	"strings"
	"testing"
)

func TestInspectLastCommitsFindsNewestAuthoredChangePerPath(t *testing.T) {
	repo := newTestRepository()
	catCode := repo.object("blob", []byte(fractchBase))
	catEdited := repo.object("blob", []byte(strings.Replace(fractchBase, "turn 15;", "turn 45;", 1)))
	stageCode := repo.object("blob", []byte("var x = 1 id \"a\";\n"))
	idle := repo.object("blob", []byte("<svg/>"))
	firstTree := repo.nestedTree(map[string]string{
		"Cat/main.fractch":    catCode,
		"Cat/assets/Idle.svg": idle,
		"Stage/main.fractch":  stageCode,
		"fractch.json":        repo.object("blob", []byte("{}")),
	})
	first := repo.commit(firstTree, "", "Initial version")
	secondTree := repo.nestedTree(map[string]string{
		"Cat/main.fractch":    catEdited,
		"Cat/assets/Idle.svg": idle,
		"Stage/main.fractch":  stageCode,
		"fractch.json":        repo.object("blob", []byte("{}")),
	})
	second := repo.commit(secondTree, first, "Change the cat")
	third := repo.commit(secondTree, second, "Message only")
	fourthTree := repo.nestedTree(map[string]string{
		"Cat/main.fractch":    catEdited,
		"Cat/assets/Idle.svg": idle,
		"Stage/main.fractch":  repo.object("blob", []byte("var x = 2 id \"a\";\n")),
		"Dog/main.fractch":    repo.object("blob", []byte(fractchBase)),
		"fractch.json":        repo.object("blob", []byte("{}")),
	})
	fourth := repo.commit(fourthTree, third, "Add a dog and save a new stage value")

	result := decodeResult(t, Inspect(repo.write(t), fourth, "last-commits", "", fourth))
	if result["ok"] != true || result["complete"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
	paths := result["paths"].(map[string]any)
	expected := map[string]string{
		"Cat":                 second,
		"Cat/main.fractch":    second,
		"Cat/assets":          first,
		"Cat/assets/Idle.svg": first,
		"Stage":               first,
		"Stage/main.fractch":  first,
		"Dog":                 fourth,
		"Dog/main.fractch":    fourth,
		"fractch.json":        first,
	}
	if len(paths) != len(expected) {
		t.Fatalf("expected %d paths, got %#v", len(expected), paths)
	}
	for path, sha := range expected {
		if paths[path] != sha {
			t.Fatalf("%s: expected %s, got %v", path, sha, paths[path])
		}
	}
	commits := result["commits"].(map[string]any)
	if _, listed := commits[third]; listed {
		t.Fatalf("a commit that changed nothing should not be listed: %#v", commits)
	}
	if commits[second].(map[string]any)["message"] != "Change the cat" {
		t.Fatalf("unexpected commit details: %#v", commits[second])
	}
}

func TestInspectLastCommitsStopsAtTheWalkLimit(t *testing.T) {
	repo := newTestRepository()
	stable := repo.object("blob", []byte("stable\n"))
	parent := ""
	head := ""
	for index := 0; index <= maxLastCommitWalk; index++ {
		tree := repo.nestedTree(map[string]string{
			"stable.txt":  stable,
			"counter.txt": repo.object("blob", []byte{byte(index), byte(index >> 8)}),
		})
		head = repo.commit(tree, parent, "Step")
		parent = head
	}
	result := decodeResult(t, Inspect(repo.write(t), head, "last-commits", "", head))
	if result["ok"] != true || result["complete"] != false {
		t.Fatalf("expected an incomplete result, got ok=%v complete=%v", result["ok"], result["complete"])
	}
	paths := result["paths"].(map[string]any)
	if paths["counter.txt"] != head {
		t.Fatalf("counter should point at the head, got %v", paths["counter.txt"])
	}
	if _, found := paths["stable.txt"]; found {
		t.Fatalf("stable.txt should be unknown past the walk limit, got %v", paths["stable.txt"])
	}
}

func TestInspectLastCommitsReportsAnExhaustedParseBudget(t *testing.T) {
	repo := newTestRepository()
	first := repo.commit(repo.nestedTree(map[string]string{
		"Cat/main.fractch": repo.object("blob", []byte(fractchBase)),
		"notes.txt":        repo.object("blob", []byte("one\n")),
	}), "", "Initial version")
	head := repo.commit(repo.nestedTree(map[string]string{
		"Cat/main.fractch": repo.object("blob", []byte(strings.Replace(fractchBase, "turn 15;", "turn 45;", 1))),
		"notes.txt":        repo.object("blob", []byte("two\n")),
	}), first, "Change the cat")
	previous := maxLastCommitParseBytes
	maxLastCommitParseBytes = 1
	defer func() { maxLastCommitParseBytes = previous }()

	result := decodeResult(t, Inspect(repo.write(t), head, "last-commits", "", head))
	if result["ok"] != true || result["complete"] != false {
		t.Fatalf("expected an incomplete result, got %#v", result)
	}
	paths := result["paths"].(map[string]any)
	if len(paths) != 0 {
		t.Fatalf("nothing should be attributed once the budget runs out, got %#v", paths)
	}
}
