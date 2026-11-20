package gitinspection

import (
	"archive/zip"
	"sort"
	"strings"
)

type fractchTargetChanges struct {
	Path     string                `json:"path"`
	Status   string                `json:"status"`
	TooLarge bool                  `json:"tooLarge,omitempty"`
	Scripts  []fractchScriptChange `json:"scripts"`
	Items    []fractchItemChange   `json:"items"`
	State    []fractchStateChange  `json:"state"`
}

type scriptCounts struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
	Moved   int `json:"moved"`
}

type assetCounts struct {
	Added    int `json:"added"`
	Removed  int `json:"removed"`
	Modified int `json:"modified"`
}

type changeTotals struct {
	Scripts     scriptCounts `json:"scripts"`
	Items       int          `json:"items"`
	State       int          `json:"state"`
	Assets      assetCounts  `json:"assets"`
	Files       int          `json:"files"`
	CodeChanged bool         `json:"codeChanged"`
}

func targetAssets(files map[string]gitFile, targetPath string) func(string) string {
	dir := ""
	if slash := strings.LastIndexByte(targetPath, '/'); slash >= 0 {
		dir = targetPath[:slash+1]
	}
	return func(relative string) string {
		return files[dir+relative].OID
	}
}

func isFractchPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".fractch")
}

func isAssetPath(path string) bool {
	return strings.HasPrefix(path, "assets/") || strings.Contains(path, "/assets/") || strings.HasPrefix(path, "fonts/")
}

func blobText(source archive, files map[string]gitFile, path string) (string, bool, bool) {
	file, ok := files[path]
	if !ok {
		return "", true, false
	}
	if file.Size > maxInlineDiffFileBytes {
		return "", false, true
	}
	kind, content, found := source.readObject(file.OID)
	if !found || kind != "blob" {
		return "", false, false
	}
	return string(content), true, false
}

func structuredChanges(changes []map[string]any, baseArchive archive, parentFiles map[string]gitFile, headArchive archive, currentFiles map[string]gitFile) ([]fractchTargetChanges, []map[string]any, changeTotals) {
	targets := []fractchTargetChanges{}
	others := []map[string]any{}
	totals := changeTotals{}
	budget := 0
	for _, change := range changes {
		path, _ := change["path"].(string)
		status, _ := change["status"].(string)
		if !isFractchPath(path) {
			if isAssetPath(path) {
				switch status {
				case "added":
					totals.Assets.Added++
				case "removed":
					totals.Assets.Removed++
				default:
					totals.Assets.Modified++
				}
			} else {
				totals.Files++
				totals.CodeChanged = totals.CodeChanged || path != "fractch.json"
			}
			others = append(others, change)
			continue
		}
		target := fractchTargetChanges{Path: path, Status: status, Scripts: []fractchScriptChange{}, Items: []fractchItemChange{}, State: []fractchStateChange{}}
		before, beforeOK, beforeLarge := blobText(baseArchive, parentFiles, path)
		after, afterOK, afterLarge := blobText(headArchive, currentFiles, path)
		size := len(before) + len(after)
		if beforeLarge || afterLarge || budget+size > maxInlineDiffTotalBytes {
			target.TooLarge = true
			totals.CodeChanged = true
			targets = append(targets, target)
			continue
		}
		if !beforeOK || !afterOK {
			target.TooLarge = true
			totals.CodeChanged = true
			targets = append(targets, target)
			continue
		}
		budget += size
		compared := compareFractchAssets(before, after, assetLookup{before: targetAssets(parentFiles, path), after: targetAssets(currentFiles, path)})
		target.Scripts, target.Items, target.State = compared.Scripts, compared.Items, compared.State
		for _, script := range target.Scripts {
			switch script.Change {
			case "added":
				totals.Scripts.Added++
			case "removed":
				totals.Scripts.Removed++
			default:
				totals.Scripts.Changed++
			}
		}
		for _, state := range target.State {
			if state.Kind == "script" && state.Field == "position" {
				totals.Scripts.Moved++
			}
		}
		totals.Items += len(target.Items)
		totals.State += len(target.State)
		if len(target.Scripts) > 0 || len(target.Items) > 0 {
			totals.CodeChanged = true
		}
		if len(target.Scripts) == 0 && len(target.Items) == 0 && len(target.State) == 0 {
			continue
		}
		targets = append(targets, target)
	}
	return targets, others, totals
}

func treeFiles(source archive, tree string) (map[string]gitFile, bool) {
	files := map[string]gitFile{}
	if tree == "" {
		return files, true
	}
	listed, ok := source.walkTree(tree)
	if !ok {
		return nil, false
	}
	for _, file := range listed {
		files[file.Path] = file
	}
	return files, true
}

func changesBetween(baseArchive archive, baseSHA string, baseCommit gitCommit, headArchive archive, headSHA string, headCommit gitCommit) string {
	parentFiles, ok := treeFiles(baseArchive, baseCommit.Tree)
	if !ok {
		return failure("parent commit tree is invalid")
	}
	currentFiles, ok := treeFiles(headArchive, headCommit.Tree)
	if !ok || headCommit.Tree == "" {
		return failure("commit tree is invalid")
	}
	changes := []map[string]any{}
	legacy := false
	for path, before := range parentFiles {
		after, exists := currentFiles[path]
		if !exists {
			changes = append(changes, map[string]any{"path": path, "status": "removed", "oldOid": before.OID, "newOid": nil, "oldSize": before.Size, "newSize": 0})
		} else if before.OID != after.OID {
			changes = append(changes, map[string]any{"path": path, "status": "modified", "oldOid": before.OID, "newOid": after.OID, "oldSize": before.Size, "newSize": after.Size})
		} else {
			continue
		}
		legacy = legacy || path == "project.sb3"
	}
	for path, after := range currentFiles {
		if _, exists := parentFiles[path]; exists {
			continue
		}
		changes = append(changes, map[string]any{"path": path, "status": "added", "oldOid": nil, "newOid": after.OID, "oldSize": 0, "newSize": after.Size})
		legacy = legacy || path == "project.sb3"
	}
	sortChanges(changes)
	targets, others, totals := structuredChanges(changes, baseArchive, parentFiles, headArchive, currentFiles)
	inlineDiffContent(others, baseArchive, parentFiles, headArchive, currentFiles)
	return encode(map[string]any{
		"ok": true, "sha": headSHA, "parent": baseSHA, "commit": headCommit, "legacy": legacy,
		"targets": targets, "files": others, "totals": totals,
	}, "could not encode commit changes")
}

func (a archive) inspectCommitChanges(sha string, commit gitCommit) string {
	if len(commit.Parents) == 0 {
		return changesBetween(archive{objects: map[string]*zip.File{}}, "", gitCommit{}, a, sha, commit)
	}
	parent := commit.Parents[0]
	parentCommit, ok := a.parseCommit(parent)
	if !ok {
		return failure("parent commit is unavailable")
	}
	return changesBetween(a, parent, parentCommit, a, sha, commit)
}

func CompareArchivesChangesJSON(basePath, baseSHA, headPath, headSHA string) string {
	baseReader, baseArchive, err := openArchive(basePath)
	if err != nil {
		return failure("base workspace archive is unavailable")
	}
	defer baseReader.Close()
	headReader, headArchive, err := openArchive(headPath)
	if err != nil {
		return failure("head workspace archive is unavailable")
	}
	defer headReader.Close()
	baseCommit, ok := baseArchive.parseCommit(baseSHA)
	if !ok {
		return failure("base commit is unavailable")
	}
	headCommit, ok := headArchive.parseCommit(headSHA)
	if !ok {
		return failure("head commit is unavailable")
	}
	return changesBetween(baseArchive, baseSHA, baseCommit, headArchive, headSHA, headCommit)
}

func sortChanges(changes []map[string]any) {
	sort.Slice(changes, func(i, j int) bool {
		return changes[i]["path"].(string) < changes[j]["path"].(string)
	})
}
