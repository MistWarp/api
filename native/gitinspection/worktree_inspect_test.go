package gitinspection

import (
	"archive/zip"
	"encoding/base64"
	"os"
	"testing"
)

func (r *testRepository) writeWithWorktree(t testing.TB, worktree map[string][]byte) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "workspace-wt-*.mwp")
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
	for path, data := range worktree {
		entry, err := writer.Create(path)
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

func TestInspectWorktreeCleanArchive(t *testing.T) {
	repository := newTestRepository()
	blob := repository.object("blob", []byte("when flagClicked {\n    move(10);\n}\n"))
	tree := repository.tree("sprite.fractch", blob)
	head := repository.commit(tree, "", "Initial version")

	path := repository.write(t)
	result := decodeResult(t, InspectWorktree(path, head, "summary", "", head))
	if result["ok"] != true || result["dirty"] != false {
		t.Fatalf("expected clean summary, got %#v", result)
	}
	changes := decodeResult(t, InspectWorktree(path, head, "changes", "", head))
	if changes["ok"] != false {
		t.Fatalf("expected failure when no worktree exists, got %#v", changes)
	}
}

func TestInspectWorktreeIdenticalToHeadIsClean(t *testing.T) {
	repository := newTestRepository()
	content := []byte("when flagClicked {\n    move(10);\n}\n")
	blob := repository.object("blob", content)
	tree := repository.tree("sprite.fractch", blob)
	head := repository.commit(tree, "", "Initial version")

	path := repository.writeWithWorktree(t, map[string][]byte{
		"sprite.fractch": content,
		"mwp.json":       []byte(`{"worktree":true}`),
	})
	result := decodeResult(t, InspectWorktree(path, head, "summary", "", head))
	if result["ok"] != true || result["dirty"] != false {
		t.Fatalf("expected identical worktree to be clean, got %#v", result)
	}
}

func TestInspectWorktreeDirtySummaryChangesInlineTreeAndFile(t *testing.T) {
	repository := newTestRepository()
	oldSrc := []byte("sprite Player {\n    when flagClicked {\n        move(10);\n    }\n}\n")
	newSrc := []byte("sprite Player {\n    when flagClicked {\n        move(25);\n    }\n}\n")
	oldBlob := repository.object("blob", oldSrc)
	playerTree := repository.tree("player.fractch", oldBlob)
	rootTree := repository.object("tree", append([]byte("40000 Player\x00"), mustDecodeHex(t, playerTree)...))
	head := repository.commit(rootTree, "", "Initial version")

	path := repository.writeWithWorktree(t, map[string][]byte{
		"Player/player.fractch": newSrc,
		"notes.txt":             []byte("draft note\n"),
		"mwp.json":              []byte(`{"worktree":true}`),
	})

	summaryRes := decodeResult(t, InspectWorktree(path, head, "summary", "", head))
	if summaryRes["ok"] != true || summaryRes["dirty"] != true || summaryRes["changedFiles"] != float64(2) {
		t.Fatalf("unexpected worktree summary: %#v", summaryRes)
	}
	summary := summaryRes["summary"].(map[string]any)
	sprites := summary["sprites"].([]any)
	if len(sprites) != 1 || sprites[0] != "Player" {
		t.Fatalf("expected Player sprite in summary, got %#v", summary)
	}

	changesRes := decodeResult(t, InspectWorktree(path, head, "changes", "", head))
	if changesRes["ok"] != true || changesRes["sha"] != "worktree" || changesRes["uncommitted"] != true || changesRes["parent"] != head {
		t.Fatalf("unexpected worktree changes header: %#v", changesRes)
	}
	targets := changesRes["targets"].([]any)
	if len(targets) != 1 || targets[0].(map[string]any)["path"] != "Player" {
		t.Fatalf("expected Player target change, got %#v", targets)
	}

	inlineRes := decodeResult(t, InspectWorktree(path, head, "commit-inline", "", head))
	if inlineRes["ok"] != true || inlineRes["uncommitted"] != true || inlineRes["additions"] != float64(2) {
		t.Fatalf("unexpected worktree inline inspection: %#v", inlineRes)
	}

	treeRes := decodeResult(t, InspectWorktree(path, head, "tree", "", head))
	if treeRes["ok"] != true || len(treeRes["files"].([]any)) != 2 {
		t.Fatalf("unexpected worktree tree: %#v", treeRes)
	}

	fileRes := decodeResult(t, InspectWorktree(path, head, "file", "notes.txt", head))
	if fileRes["ok"] != true {
		t.Fatalf("unexpected worktree file result: %#v", fileRes)
	}
	decoded, err := base64.StdEncoding.DecodeString(fileRes["content"].(string))
	if err != nil || string(decoded) != "draft note\n" {
		t.Fatalf("unexpected worktree file content: %q err=%v", string(decoded), err)
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	out := make([]byte, len(value)/2)
	for i := 0; i < len(out); i++ {
		hi := fromHex(value[i*2])
		lo := fromHex(value[i*2+1])
		out[i] = (hi << 4) | lo
	}
	return out
}

func fromHex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}
