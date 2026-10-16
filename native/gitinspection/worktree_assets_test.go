package gitinspection

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeWorktreeFixture(t *testing.T, files map[string][]byte, manifest map[string]any) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "archive.mwp")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, data := range files {
		d, _ := w.Create(name)
		d.Write(data)
	}
	if manifest != nil {
		d, _ := w.Create("mwp.json")
		json.NewEncoder(d).Encode(manifest)
	}
	w.Close()
	f.Close()
	return p
}

func readWorktreeFixture(t *testing.T, path string) (map[string][]byte, map[string]any) {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	files := map[string][]byte{}
	manifest := map[string]any{}
	for _, f := range r.File {
		s, _ := f.Open()
		b, _ := io.ReadAll(s)
		s.Close()
		if f.Name == "mwp.json" {
			json.Unmarshal(b, &manifest)
			continue
		}
		files[f.Name] = b
	}
	return files, manifest
}

func md5ext(data []byte, ext string) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:]) + "." + ext
}

func TestMaterializeWorktreeAssetsFromBaseAndAssetDirs(t *testing.T) {
	kept := []byte("unchanged costume")
	renamed := []byte("renamed sound")
	added := []byte("new costume")
	base := writeWorktreeFixture(t, map[string][]byte{
		"Sprite1/assets/costume1.png": kept,
		"Stage/assets/pop.wav":        renamed,
		".git/HEAD":                   []byte("ref: refs/heads/main\n"),
	}, map[string]any{"head": "x"})
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, md5ext(added, "png")), added, 0o644)
	incoming := writeWorktreeFixture(t, map[string][]byte{
		"Sprite1/code.fractch": []byte("code"),
	}, map[string]any{"head": "y", "worktreeAssets": map[string]any{
		"Sprite1/assets/costume1.png": md5ext(kept, "png"),
		"Sprite1/assets/pop.wav":      md5ext(renamed, "wav"),
		"Sprite1/assets/costume2.png": md5ext(added, "png"),
	}})
	out := filepath.Join(t.TempDir(), "out.mwp")
	result := MaterializeWorktreeAssets(incoming, out, base, "/nonexistent\n"+dir)
	if !result.OK || result.Refs != 3 {
		t.Fatalf("unexpected result %#v", result)
	}
	files, manifest := readWorktreeFixture(t, out)
	if string(files["Sprite1/assets/costume1.png"]) != string(kept) || string(files["Sprite1/assets/pop.wav"]) != string(renamed) ||
		string(files["Sprite1/assets/costume2.png"]) != string(added) || string(files["Sprite1/code.fractch"]) != "code" {
		t.Fatalf("assets not materialized: %v", files)
	}
	if _, ok := manifest["worktreeAssets"]; ok || manifest["head"] != "y" {
		t.Fatalf("manifest not rewritten: %v", manifest)
	}
}

func TestMaterializeWorktreeAssetsReportsMissingAndRejectsUnsafe(t *testing.T) {
	data := []byte("costume")
	wrong := t.TempDir()
	os.WriteFile(filepath.Join(wrong, md5ext(data, "png")), []byte("tampered"), 0o644)
	incoming := writeWorktreeFixture(t, map[string][]byte{}, map[string]any{"worktreeAssets": map[string]any{
		"Sprite1/assets/a.png": md5ext(data, "png"),
	}})
	out := filepath.Join(t.TempDir(), "out.mwp")
	result := MaterializeWorktreeAssets(incoming, out, "", wrong)
	if result.OK || len(result.Missing) != 1 || result.Missing[0] != md5ext(data, "png") {
		t.Fatalf("expected the tampered asset to be missing: %#v", result)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("wrote output for an incomplete archive")
	}
	for _, refs := range []map[string]any{
		{"../escape.png": md5ext(data, "png")},
		{".git/objects/aa/bb": md5ext(data, "png")},
		{"mwp.json": md5ext(data, "png")},
		{"a.png": "../../etc/passwd"},
		{"existing.png": md5ext(data, "png")},
	} {
		p := writeWorktreeFixture(t, map[string][]byte{"existing.png": data}, map[string]any{"worktreeAssets": refs})
		if MaterializeWorktreeAssets(p, out, "", wrong).OK {
			t.Fatalf("accepted unsafe references %v", refs)
		}
	}
}

func TestMaterializeWorktreeAssetsWithoutReferencesIsUnchanged(t *testing.T) {
	incoming := writeWorktreeFixture(t, map[string][]byte{"a.png": []byte("x")}, map[string]any{"head": "y"})
	out := filepath.Join(t.TempDir(), "out.mwp")
	result := MaterializeWorktreeAssets(incoming, out, "", "")
	if !result.OK || result.Refs != 0 {
		t.Fatalf("unexpected result %#v", result)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("rewrote an archive without references")
	}
}
