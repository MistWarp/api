package gitinspection

import (
	"archive/zip"
	"sort"
	"strings"
)

type fractchTargetChanges struct {
	Path     string                `json:"path"`
	Status   string                `json:"status"`
	Files    []string              `json:"files"`
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
	Scripts   scriptCounts `json:"scripts"`
	Items     int          `json:"items"`
	State     int          `json:"state"`
	Assets    assetCounts  `json:"assets"`
	Files     int          `json:"files"`
	StateOnly bool         `json:"stateOnly"`
}

func pathDir(path string) string {
	if slash := strings.LastIndexByte(path, '/'); slash >= 0 {
		return path[:slash]
	}
	return ""
}

func spriteDir(path string) string {
	if index := strings.Index(path, "/assets/"); index >= 0 {
		return path[:index]
	}
	if strings.HasPrefix(path, "assets/") {
		return ""
	}
	return pathDir(path)
}

func dirAssets(files map[string]gitFile, dir string) func(string) string {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	return func(relative string) string {
		return files[prefix+relative].OID
	}
}

func isFractchPath(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".fractch")
}

func isAssetPath(path string) bool {
	return strings.HasPrefix(path, "assets/") || strings.Contains(path, "/assets/") || strings.HasPrefix(path, "fonts/")
}

func dirFractchFiles(files map[string]gitFile, dir string) []string {
	paths := []string{}
	for path := range files {
		if isFractchPath(path) && pathDir(path) == dir {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func readSources(source archive, files map[string]gitFile, paths []string, budget *int) ([]fractchSource, bool) {
	sources := []fractchSource{}
	for _, path := range paths {
		file := files[path]
		if file.Size > maxInlineDiffFileBytes || *budget+file.Size > maxInlineDiffTotalBytes {
			return nil, false
		}
		kind, content, found := source.readObject(file.OID)
		if !found || kind != "blob" {
			return nil, false
		}
		*budget += file.Size
		sources = append(sources, fractchSource{Path: path, Text: string(content)})
	}
	return sources, true
}

func (totals *changeTotals) countTarget(target fractchTargetChanges) {
	for _, script := range target.Scripts {
		switch script.Change {
		case "added":
			totals.Scripts.Added++
		case "removed":
			totals.Scripts.Removed++
		case "moved":
			totals.Scripts.Moved++
		default:
			totals.Scripts.Changed++
		}
	}
	totals.Items += len(target.Items)
	totals.State += len(target.State)
}

func structuredChanges(changes []map[string]any, baseArchive archive, parentFiles map[string]gitFile, headArchive archive, currentFiles map[string]gitFile) ([]fractchTargetChanges, []map[string]any, changeTotals) {
	others := []map[string]any{}
	totals := changeTotals{}
	dirs := map[string]bool{}
	authoredFiles := 0
	for _, change := range changes {
		path, _ := change["path"].(string)
		status, _ := change["status"].(string)
		if isFractchPath(path) {
			dirs[pathDir(path)] = true
			continue
		}
		others = append(others, change)
		if isAssetPath(path) {
			dirs[spriteDir(path)] = true
			switch status {
			case "added":
				totals.Assets.Added++
			case "removed":
				totals.Assets.Removed++
			default:
				totals.Assets.Modified++
			}
			continue
		}
		totals.Files++
		if path != "fractch.json" {
			authoredFiles++
		}
	}
	ordered := []string{}
	for dir := range dirs {
		ordered = append(ordered, dir)
	}
	sort.Strings(ordered)
	targets := []fractchTargetChanges{}
	budget := 0
	tooLarge := false
	for _, dir := range ordered {
		beforePaths := dirFractchFiles(parentFiles, dir)
		afterPaths := dirFractchFiles(currentFiles, dir)
		if len(beforePaths) == 0 && len(afterPaths) == 0 {
			continue
		}
		status := "modified"
		if len(beforePaths) == 0 {
			status = "added"
		} else if len(afterPaths) == 0 {
			status = "removed"
		}
		listed := afterPaths
		if len(listed) == 0 {
			listed = beforePaths
		}
		target := fractchTargetChanges{Path: dir, Status: status, Files: listed, Scripts: []fractchScriptChange{}, Items: []fractchItemChange{}, State: []fractchStateChange{}}
		before, beforeOK := readSources(baseArchive, parentFiles, beforePaths, &budget)
		after, afterOK := readSources(headArchive, currentFiles, afterPaths, &budget)
		if !beforeOK || !afterOK {
			target.TooLarge = true
			tooLarge = true
			targets = append(targets, target)
			continue
		}
		compared := compareFractchSources(before, after, assetLookup{before: dirAssets(parentFiles, dir), after: dirAssets(currentFiles, dir)})
		target.Scripts, target.Items, target.State = compared.Scripts, compared.Items, compared.State
		if len(target.Scripts) == 0 && len(target.Items) == 0 && len(target.State) == 0 {
			continue
		}
		totals.countTarget(target)
		targets = append(targets, target)
	}
	scripts := totals.Scripts.Added + totals.Scripts.Removed + totals.Scripts.Changed + totals.Scripts.Moved
	assets := totals.Assets.Added + totals.Assets.Removed + totals.Assets.Modified
	totals.StateOnly = totals.State > 0 && scripts == 0 && totals.Items == 0 && authoredFiles == 0 && assets == 0 && !tooLarge
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
