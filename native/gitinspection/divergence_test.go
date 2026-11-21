package gitinspection

import "testing"

func TestCompareDivergenceAheadBehindAndUpToDate(t *testing.T) {
	parentRepo := newTestRepository()
	blob1 := parentRepo.object("blob", []byte("v1\n"))
	tree1 := parentRepo.tree("main.fractch", blob1)
	base := parentRepo.commit(tree1, "", "Base commit")

	blob2 := parentRepo.object("blob", []byte("v2\n"))
	tree2 := parentRepo.tree("main.fractch", blob2)
	p1 := parentRepo.commit(tree2, base, "Parent commit 1")

	blob3 := parentRepo.object("blob", []byte("v3\n"))
	tree3 := parentRepo.tree("main.fractch", blob3)
	p2 := parentRepo.commit(tree3, p1, "Parent commit 2")

	childRepo := newTestRepository()
	for oid, data := range parentRepo.objects {
		if oid == base || oid == tree1 || oid == blob1 {
			childRepo.objects[oid] = data
		}
	}
	cBlob1 := childRepo.object("blob", []byte("child1\n"))
	cTree1 := childRepo.tree("main.fractch", cBlob1)
	c1 := childRepo.commit(cTree1, base, "Child commit 1")
	cBlob2 := childRepo.object("blob", []byte("child2\n"))
	cTree2 := childRepo.tree("main.fractch", cBlob2)
	c2 := childRepo.commit(cTree2, c1, "Child commit 2")
	cBlob3 := childRepo.object("blob", []byte("child3\n"))
	cTree3 := childRepo.tree("main.fractch", cBlob3)
	c3 := childRepo.commit(cTree3, c2, "Child commit 3")

	parentPath := parentRepo.write(t)
	childPath := childRepo.write(t)

	diverged := decodeResult(t, CompareDivergence(childPath, c3, parentPath, p2))
	if diverged["ok"] != true || diverged["commonAncestor"] != true || diverged["ahead"] != float64(3) || diverged["behind"] != float64(2) {
		t.Fatalf("expected 3 ahead 2 behind, got %#v", diverged)
	}

	upToDate := decodeResult(t, CompareDivergence(childPath, base, parentPath, base))
	if upToDate["ok"] != true || upToDate["commonAncestor"] != true || upToDate["ahead"] != float64(0) || upToDate["behind"] != float64(0) {
		t.Fatalf("expected 0 ahead 0 behind, got %#v", upToDate)
	}

	onlyBehind := decodeResult(t, CompareDivergence(childPath, base, parentPath, p2))
	if onlyBehind["ok"] != true || onlyBehind["commonAncestor"] != true || onlyBehind["ahead"] != float64(0) || onlyBehind["behind"] != float64(2) {
		t.Fatalf("expected 0 ahead 2 behind, got %#v", onlyBehind)
	}

	unrelatedRepo := newTestRepository()
	uBlob := unrelatedRepo.object("blob", []byte("other\n"))
	uTree := unrelatedRepo.tree("other.fractch", uBlob)
	uHead := unrelatedRepo.commit(uTree, "", "Unrelated")
	unrelatedPath := unrelatedRepo.write(t)

	unrelated := decodeResult(t, CompareDivergence(unrelatedPath, uHead, parentPath, p2))
	if unrelated["ok"] != true || unrelated["commonAncestor"] != false {
		t.Fatalf("expected no common ancestor, got %#v", unrelated)
	}
}
