package gitinspection

import (
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func historyFixture(t *testing.T, r *testRepository, refs map[string]string, branch, base string, omit string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "history.mwp")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	w := zip.NewWriter(f)
	add := func(n string, b []byte) {
		d, e := w.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = d.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	m := map[string]any{"format": "mistwarp-project", "version": 1, "branch": branch, "head": refs[branch], "delta": base != "", "baseHead": nil, "worktree": false, "commits": []any{}, "graph": map[string]any{}}
	if base != "" {
		m["baseHead"] = base
	}
	b, _ := json.Marshal(m)
	add("mwp.json", b)
	add(".git/HEAD", []byte("ref: refs/heads/"+branch+"\n"))
	for b, h := range refs {
		add(".git/refs/heads/"+b, []byte(h+"\n"))
	}
	for oid, b := range r.objects {
		if oid != omit {
			add(".git/objects/"+oid[:2]+"/"+oid[2:], b)
		}
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return p
}
func assertHistory(t *testing.T, path, head, ancestor, tree string, n int) {
	t.Helper()
	r, a, _, m, refs, branch, e := readHistoryArchive(path)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if refs[branch] != head || m["head"] != head {
		t.Fatal("head mismatch")
	}
	if ancestor != "" && !a.reachable(ancestor, head) {
		t.Fatal("ancestry lost")
	}
	c, ok := a.parseCommit(head)
	if !ok || c.Tree != tree {
		t.Fatal("playable tree changed")
	}
	if _, _, e := repositoryHistory(a, refs, branch); e != nil {
		t.Fatal(e)
	}
	if len(m["commits"].([]any)) != n {
		t.Fatalf("history not visible: %v", m["commits"])
	}
	response := decodeResult(t, Inspect(path, head, "tree", "", head))
	if response["ok"] != true {
		t.Fatal(response)
	}
}
func TestValidateFullAndDeltaHistory(t *testing.T) {
	r := newTestRepository()
	blob := r.object("blob", []byte("old"))
	tree := r.tree("main.fractch", blob)
	base := r.commit(tree, "", "base")
	stored := historyFixture(t, r, map[string]string{"main": base}, "main", "", "")
	newBlob := r.object("blob", []byte("new"))
	newTree := r.tree("main.fractch", newBlob)
	head := r.commit(newTree, base, "save")
	for _, missing := range []string{base, tree, blob} {
		t.Run("missing-"+missing[:8], func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "out.mwp")
			incoming := historyFixture(t, r, map[string]string{"main": head}, "main", "", missing)
			result := ValidateHistoryArchive(stored, incoming, output, base, base, "")
			if result.OK {
				t.Fatal("accepted incomplete full history")
			}
			if _, e := os.Stat(output); !os.IsNotExist(e) {
				t.Fatal("partial publication")
			}
		})
	}
	full := historyFixture(t, r, map[string]string{"main": head}, "main", "", "")
	output := filepath.Join(t.TempDir(), "full.mwp")
	result := ValidateHistoryArchive(stored, full, output, base, base, "")
	if !result.OK {
		t.Fatal(result.Error)
	}
	assertHistory(t, output, head, base, newTree, 2)
	// The same partial archive is valid only when explicitly declared a delta.
	delta := newTestRepository()
	for _, oid := range []string{head, newTree, newBlob} {
		delta.objects[oid] = r.objects[oid]
	}
	incoming := historyFixture(t, delta, map[string]string{"main": head}, "main", base, "")
	output = filepath.Join(t.TempDir(), "delta.mwp")
	result = ValidateHistoryArchive(stored, incoming, output, base, base, "")
	if !result.OK {
		t.Fatal(result.Error)
	}
	assertHistory(t, output, head, base, newTree, 2)
	for _, metadata := range []string{`{"head":"wrong"}`, `{"head":{}}`, `[]`, `invalid`} {
		if ValidateHistoryArchive(stored, incoming, filepath.Join(t.TempDir(), "bad"), base, base, metadata).OK {
			t.Fatal("accepted conflicting metadata")
		}
	}
	for _, missing := range []string{newTree, newBlob} {
		p := historyFixture(t, delta, map[string]string{"main": head}, "main", base, missing)
		if ValidateHistoryArchive(stored, p, filepath.Join(t.TempDir(), "bad"), base, base, "").OK {
			t.Fatal("accepted incomplete delta")
		}
	}
	unrelated := r.commit(newTree, "", "unrelated")
	p := historyFixture(t, r, map[string]string{"main": unrelated}, "main", "", "")
	if ValidateHistoryArchive(stored, p, filepath.Join(t.TempDir(), "bad"), base, base, "").OK {
		t.Fatal("accepted disconnected root")
	}
}
func TestHistoryCompactionAndCaps(t *testing.T) {
	r := newTestRepository()
	tree := r.tree("main.fractch", r.object("blob", []byte("playable")))
	head := r.commit(tree, "", "0")
	base := head
	stored := historyFixture(t, r, map[string]string{"main": head}, "main", "", "")
	for i := 1; i <= 55; i++ {
		next := r.commit(tree, head, fmt.Sprint(i))
		delta := newTestRepository()
		delta.objects[next] = r.objects[next]
		incoming := historyFixture(t, delta, map[string]string{"main": next}, "main", head, "")
		out := filepath.Join(t.TempDir(), "compacted.mwp")
		v := ValidateHistoryArchive(stored, incoming, out, head, base, "")
		if !v.OK {
			t.Fatal(i, v.Error)
		}
		head = next
		stored = out
	}
	assertHistory(t, stored, head, base, tree, 50)
}
func TestForkBranchRestoreAndNestedGeneration(t *testing.T) {
	r := newTestRepository()
	tree := r.tree("main.fractch", r.object("blob", []byte("old")))
	base := r.commit(tree, "", "base")
	main := r.commit(tree, base, "main")
	featureTree := r.tree("main.fractch", r.object("blob", []byte("feature")))
	feature := r.commit(featureTree, base, "feature")
	source := historyFixture(t, r, map[string]string{"main": main, "feature": feature}, "main", "", "")
	out := filepath.Join(t.TempDir(), "fork.mwp")
	fork := ForkHistoryArchive(source, out, "feature", "fork", "parent")
	if !fork.OK {
		t.Fatal(fork.Error)
	}
	assertHistory(t, out, feature, base, featureTree, 2)
	if fork.Manifest["baseCommit"] != feature {
		t.Fatal("wrong fork base")
	}
	restored := filepath.Join(t.TempDir(), "restore.mwp")
	res := RestoreArchive(out, feature, base, restored, "fork", "parent", feature, "feature", "Mist", "restore", 1)
	if !res.OK {
		t.Fatal(res.Error)
	}
	canonical := filepath.Join(t.TempDir(), "restore-full.mwp")
	v := ValidateHistoryArchive(out, restored, canonical, feature, feature, "")
	if !v.OK {
		t.Fatal(v.Error)
	}
	assertHistory(t, canonical, res.Head, feature, tree, 3)
	// Generate twice with a recorded multi-commit base, including a nested remix.
	project := filepath.Join(t.TempDir(), "project.json")
	f, _ := os.Create(project)
	gz := gzip.NewWriter(f)
	gz.Write([]byte(`{"targets":[]}`))
	gz.Close()
	f.Close()
	assets := t.TempDir()
	parentPath := source
	parentHead := feature
	for i := 0; i < 2; i++ {
		p := filepath.Join(t.TempDir(), "generated.mwp")
		v := decodeResult(t, GenerateHistoryArchiveJSON(project, assets, p, fmt.Sprint(i), "Mist", 1000+float64(i), parentPath, parentHead))
		if v["ok"] != true {
			t.Fatal(v)
		}
		rr, a, _, m, _, _, e := readHistoryArchive(p)
		if e != nil {
			t.Fatal(e)
		}
		head := m["head"].(string)
		c, _ := a.parseCommit(head)
		if len(c.Parents) != 1 || c.Parents[0] != parentHead {
			t.Fatal("wrong migration parent")
		}
		rr.Close()
		assertHistory(t, p, head, feature, c.Tree, 3+i)
		parentPath = p
		parentHead = head
	}
}
func TestManifestHeadConflictAndWorktreePreservation(t *testing.T) {
	r := newTestRepository()
	tree := r.tree("file", r.object("blob", []byte("bytes")))
	head := r.commit(tree, "", "root")
	p := historyFixture(t, r, map[string]string{"main": head}, "main", "", "")
	zr, _, files, m, _, _, e := readHistoryArchive(p)
	if e != nil {
		t.Fatal(e)
	}
	defer zr.Close()
	m["head"] = "wrong"
	bad := filepath.Join(t.TempDir(), "bad.mwp")
	if e = writeValidatedArchive(bad, files, m); e != nil {
		t.Fatal(e)
	}
	if ValidateHistoryArchive("", bad, filepath.Join(t.TempDir(), "out"), "", "", "").OK {
		t.Fatal("accepted stale manifest")
	}
	// Canonicalization copies existing archive entries byte-for-byte.
	m["head"] = head
	out := filepath.Join(t.TempDir(), "good.mwp")
	if e = writeValidatedArchive(out, files, m); e != nil {
		t.Fatal(e)
	}
	rd, e := zip.OpenReader(out)
	if e != nil {
		t.Fatal(e)
	}
	defer rd.Close()
	for _, f := range rd.File {
		if old := files[f.Name]; old != nil && f.Name != "mwp.json" {
			a, _ := f.Open()
			b, _ := old.Open()
			aa, _ := io.ReadAll(a)
			bb, _ := io.ReadAll(b)
			a.Close()
			b.Close()
			if string(aa) != string(bb) {
				t.Fatalf("changed %s", f.Name)
			}
		}
	}
}

func TestMergeValidationPreservesBothParentsAndOtherBranches(t *testing.T) {
	r := newTestRepository()
	tree := r.tree("file", r.object("blob", []byte("base")))
	base := r.commit(tree, "", "base")
	target := r.commit(tree, base, "target")
	other := r.commit(tree, base, "other")
	sourceTree := r.tree("file", r.object("blob", []byte("source")))
	source := r.commit(sourceTree, target, "source")
	targetPath := historyFixture(t, r, map[string]string{"main": target, "other": other}, "main", "", "")
	sourcePath := historyFixture(t, r, map[string]string{"main": source}, "main", "", "")
	delta := filepath.Join(t.TempDir(), "merge.mwp")
	merge := CreateFastForwardMergeArchive(targetPath, target, sourcePath, source, delta, "target", "", "", "main", "Mist", "merge", 1)
	if !merge.OK {
		t.Fatal(merge.Error)
	}
	out := filepath.Join(t.TempDir(), "full.mwp")
	v := ValidateHistoryArchive(targetPath, delta, out, target, "", "")
	if !v.OK {
		t.Fatal(v.Error)
	}
	assertHistory(t, out, merge.Head, target, sourceTree, 4)
	rr, a, _, _, refs, _, e := readHistoryArchive(out)
	if e != nil {
		t.Fatal(e)
	}
	defer rr.Close()
	if !a.reachable(source, merge.Head) || refs["other"] != other {
		t.Fatal("merge lost source or another branch")
	}
}

func TestGraphCapStillValidatesOlderObjects(t *testing.T) {
	r := newTestRepository()
	blob := r.object("blob", []byte("base"))
	tree := r.tree("file", blob)
	head := r.commit(tree, "", "base")
	base := head
	for i := 0; i < 205; i++ {
		head = r.commit(tree, head, fmt.Sprint(i))
	}
	in := historyFixture(t, r, map[string]string{"main": head}, "main", "", "")
	out := filepath.Join(t.TempDir(), "full.mwp")
	v := ValidateHistoryArchive("", in, out, "", "", "")
	if !v.OK {
		t.Fatal(v.Error)
	}
	if len(v.Manifest["commits"].([]any)) != 50 || len(v.Manifest["graph"].(map[string]any)["nodes"].([]any)) != 200 {
		t.Fatal("metadata caps changed")
	}
	missing := historyFixture(t, r, map[string]string{"main": head}, "main", "", base)
	if ValidateHistoryArchive("", missing, filepath.Join(t.TempDir(), "bad"), "", "", "").OK {
		t.Fatal("cap hid missing ancestor")
	}
}

func TestFullHistoryReplacementDropsOldBranches(t *testing.T) {
	old := newTestRepository()
	oldTree := old.tree("main.fractch", old.object("blob", []byte("old")))
	oldHead := old.commit(oldTree, "", "old")
	stored := historyFixture(t, old, map[string]string{"main": oldHead, "old-branch": oldHead}, "main", "", "")
	imported := newTestRepository()
	tree := imported.tree("main.fractch", imported.object("blob", []byte("imported")))
	base := imported.commit(tree, "", "imported base")
	head := imported.commit(tree, base, "imported head")
	incoming := historyFixture(t, imported, map[string]string{"custom": head, "release": base}, "custom", "", "")
	output := filepath.Join(t.TempDir(), "replacement.mwp")
	if ValidateHistoryArchive(stored, incoming, output, oldHead, "", "").OK {
		t.Fatal("ordinary save accepted unrelated history")
	}
	// The explicit replacement upload supplies no stored archive or ancestry constraints.
	result := ValidateHistoryArchive("", incoming, output, "", "", "", "saved-project", "")
	if !result.OK {
		t.Fatal(result.Error)
	}
	assertHistory(t, output, head, base, tree, 2)
	r, _, _, _, refs, branch, err := readHistoryArchive(output)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if branch != "custom" || len(refs) != 2 || refs["release"] != base || refs["old-branch"] != "" {
		t.Fatalf("replacement retained old refs or lost imported refs: %v", refs)
	}
	for _, bad := range []string{
		historyFixture(t, imported, map[string]string{"custom": head}, "custom", base, ""),
		historyFixture(t, imported, map[string]string{"custom": head}, "custom", "", base),
	} {
		if ValidateHistoryArchive("", bad, filepath.Join(t.TempDir(), "bad.mwp"), "", "", "").OK {
			t.Fatal("replacement accepted a delta or incomplete history")
		}
	}
}
