package gitinspection

import (
	"archive/zip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const maxWorktreeAssetRefs = 20_000

var worktreeAssetName = regexp.MustCompile(`^[0-9a-f]{32}\.[a-z0-9]{1,8}$`)

type worktreeAssetsResult struct {
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Missing []string `json:"missing,omitempty"`
	Refs    int      `json:"refs"`
}

type worktreeAssetSource struct {
	entry *zip.File
	path  string
}

func safeWorktreePath(name string) bool {
	if name == "" || len(name) > 1024 || name == "mwp.json" || strings.HasPrefix(name, ".git/") || strings.HasPrefix(name, "/") ||
		strings.ContainsAny(name, "\\\x00") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func entryMD5(f *zip.File) string {
	s, err := f.Open()
	if err != nil {
		return ""
	}
	defer s.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(s, maxObjectBytes)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileMD5(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxObjectBytes)); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func compressWorktreeAsset(name string) uint16 {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".svg", ".wav", ".json", ".ttf", ".otf":
		return zip.Deflate
	}
	return zip.Store
}

func MaterializeWorktreeAssets(incomingPath, outputPath, basePath, assetDirs string) worktreeAssetsResult {
	incoming, err := zip.OpenReader(incomingPath)
	if err != nil {
		return worktreeAssetsResult{Error: "invalid history archive"}
	}
	defer incoming.Close()
	present := map[string]bool{}
	var manifestEntry *zip.File
	for _, f := range incoming.File {
		present[f.Name] = true
		if f.Name == "mwp.json" {
			manifestEntry = f
		}
	}
	if manifestEntry == nil {
		return worktreeAssetsResult{Error: "invalid manifest"}
	}
	s, err := manifestEntry.Open()
	if err != nil {
		return worktreeAssetsResult{Error: "invalid manifest"}
	}
	raw, err := io.ReadAll(io.LimitReader(s, maxObjectBytes))
	s.Close()
	manifest := map[string]any{}
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		return worktreeAssetsResult{Error: "invalid manifest"}
	}
	refsValue, hasRefs := manifest["worktreeAssets"]
	if !hasRefs || refsValue == nil {
		return worktreeAssetsResult{OK: true}
	}
	refs, ok := refsValue.(map[string]any)
	if !ok || len(refs) > maxWorktreeAssetRefs {
		return worktreeAssetsResult{Error: "invalid worktree asset references"}
	}
	paths := make([]string, 0, len(refs))
	for path, value := range refs {
		name, ok := value.(string)
		if !ok || !worktreeAssetName.MatchString(name) || !safeWorktreePath(path) || present[path] {
			return worktreeAssetsResult{Error: "invalid worktree asset references"}
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)

	baseEntries := map[string]*zip.File{}
	if basePath != "" {
		base, err := zip.OpenReader(basePath)
		if err != nil {
			return worktreeAssetsResult{Error: "stored history unavailable"}
		}
		defer base.Close()
		for _, f := range base.File {
			if safeWorktreePath(f.Name) && !f.FileInfo().IsDir() {
				baseEntries[f.Name] = f
			}
		}
	}
	hashed := map[*zip.File]string{}
	hashOf := func(f *zip.File) string {
		if h, ok := hashed[f]; ok {
			return h
		}
		h := entryMD5(f)
		hashed[f] = h
		return h
	}
	var byHash map[string]*zip.File
	dirs := []string{}
	for _, dir := range strings.Split(assetDirs, "\n") {
		if dir = strings.TrimSpace(dir); dir != "" {
			dirs = append(dirs, dir)
		}
	}
	sources := map[string]worktreeAssetSource{}
	missing := []string{}
	for _, path := range paths {
		name := refs[path].(string)
		digest := name[:32]
		if f := baseEntries[path]; f != nil && hashOf(f) == digest {
			sources[path] = worktreeAssetSource{entry: f}
			continue
		}
		if byHash == nil {
			byHash = map[string]*zip.File{}
			for _, f := range baseEntries {
				if h := hashOf(f); h != "" && byHash[h] == nil {
					byHash[h] = f
				}
			}
		}
		if f := byHash[digest]; f != nil {
			sources[path] = worktreeAssetSource{entry: f}
			continue
		}
		found := false
		for _, dir := range dirs {
			candidate := filepath.Join(dir, name)
			if fileMD5(candidate) == digest {
				sources[path] = worktreeAssetSource{path: candidate}
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return worktreeAssetsResult{Error: "worktree assets unavailable", Missing: missing}
	}
	delete(manifest, "worktreeAssets")
	if err := writeMaterializedArchive(outputPath, incoming.File, manifest, paths, sources); err != nil {
		return worktreeAssetsResult{Error: err.Error()}
	}
	return worktreeAssetsResult{OK: true, Refs: len(paths)}
}

func writeMaterializedArchive(path string, files []*zip.File, manifest map[string]any, paths []string, sources map[string]worktreeAssetSource) (err error) {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	w := zip.NewWriter(out)
	defer func() {
		if e := w.Close(); err == nil {
			err = e
		}
		if e := out.Close(); err == nil {
			err = e
		}
		if err != nil {
			os.Remove(path)
		}
	}()
	copyRaw := func(name string, f *zip.File) error {
		header := f.FileHeader
		header.Name = name
		s, e := f.OpenRaw()
		if e != nil {
			return e
		}
		d, e := w.CreateRaw(&header)
		if e != nil {
			return e
		}
		_, e = io.Copy(d, s)
		return e
	}
	for _, f := range files {
		if f.Name == "mwp.json" {
			continue
		}
		if err := copyRaw(f.Name, f); err != nil {
			return err
		}
	}
	for _, name := range paths {
		source := sources[name]
		if source.entry != nil {
			if err := copyRaw(name, source.entry); err != nil {
				return err
			}
			continue
		}
		in, e := os.Open(source.path)
		if e != nil {
			return e
		}
		d, e := w.CreateHeader(&zip.FileHeader{Name: name, Method: compressWorktreeAsset(name)})
		if e == nil {
			_, e = io.Copy(d, in)
		}
		in.Close()
		if e != nil {
			return e
		}
	}
	d, err := w.Create("mwp.json")
	if err != nil {
		return err
	}
	return json.NewEncoder(d).Encode(manifest)
}

func MaterializeWorktreeAssetsJSON(incomingPath, outputPath, basePath, assetDirs string) string {
	b, _ := json.Marshal(MaterializeWorktreeAssets(incomingPath, outputPath, basePath, assetDirs))
	return string(b)
}
