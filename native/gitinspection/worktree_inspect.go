package gitinspection

import (
	"archive/zip"
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strconv"
)

func gitBlobOID(content []byte) string {
	hash := sha1.New()
	hash.Write([]byte("blob "))
	hash.Write([]byte(strconv.Itoa(len(content))))
	hash.Write([]byte{0})
	hash.Write(content)
	return hex.EncodeToString(hash.Sum(nil))
}

func (a archive) worktreeFiles() (map[string]gitFile, archive, bool) {
	if len(a.worktree) == 0 || len(a.worktree) > maxTreeFiles {
		return nil, archive{}, false
	}
	files := make(map[string]gitFile, len(a.worktree))
	byOID := make(map[string]*zip.File, len(a.worktree))
	for path, item := range a.worktree {
		content, ok := readWorktreeEntry(item)
		if !ok {
			return nil, archive{}, false
		}
		oid := gitBlobOID(content)
		info := blobSummary(content)
		files[path] = gitFile{
			Path:   path,
			OID:    oid,
			Mode:   "100644",
			Size:   info.size,
			Binary: info.binary,
		}
		byOID[oid] = item
	}
	clone := a
	clone.worktreeByOID = byOID
	return files, clone, true
}

func diffFileMaps(parentFiles, currentFiles map[string]gitFile) ([]map[string]any, int, int, bool) {
	changes := []map[string]any{}
	additions, deletions := 0, 0
	legacy := false
	for path, before := range parentFiles {
		after, exists := currentFiles[path]
		if !exists {
			changes = append(changes, map[string]any{
				"path": path, "status": "removed", "oldOid": before.OID, "newOid": nil,
				"oldSize": before.Size, "newSize": 0,
			})
			deletions++
			legacy = legacy || path == "project.sb3"
		} else if before.OID != after.OID {
			changes = append(changes, map[string]any{
				"path": path, "status": "modified", "oldOid": before.OID, "newOid": after.OID,
				"oldSize": before.Size, "newSize": after.Size,
			})
			additions++
			deletions++
			legacy = legacy || path == "project.sb3"
		}
	}
	for path, after := range currentFiles {
		if _, exists := parentFiles[path]; exists {
			continue
		}
		changes = append(changes, map[string]any{
			"path": path, "status": "added", "oldOid": nil, "newOid": after.OID,
			"oldSize": 0, "newSize": after.Size,
		})
		additions++
		legacy = legacy || path == "project.sb3"
	}
	sort.Slice(changes, func(i, j int) bool {
		return changes[i]["path"].(string) < changes[j]["path"].(string)
	})
	return changes, additions, deletions, legacy
}

func InspectWorktree(workspacePath, headSHA, operation, requestedPath, allowedHeads string) string {
	releaseSlot := acquireInspectionSlot()
	defer releaseSlot()
	shared, err := acquireSharedArchive(workspacePath)
	if err != nil {
		return failure("workspace archive is unavailable")
	}
	defer shared.release()
	baseArchive := shared.archive

	if len(baseArchive.worktree) == 0 {
		if operation == "summary" {
			return encode(map[string]any{"ok": true, "dirty": false}, "could not encode worktree summary")
		}
		return failure("no uncommitted changes in this workspace")
	}

	headCommit, ok := baseArchive.parseCommit(headSHA)
	if !ok {
		return failure("commit was not found in this workspace")
	}
	if !baseArchive.reachable(headSHA, allowedHeads) {
		return failure("commit is not reachable from this project's history")
	}

	if operation == "file" {
		item := baseArchive.worktree[requestedPath]
		if item == nil {
			return failure("file was not found in uncommitted changes")
		}
		content, ok := readWorktreeEntry(item)
		if !ok {
			return failure("file object is unavailable")
		}
		info := blobSummary(content)
		file := gitFile{
			Path:   requestedPath,
			OID:    gitBlobOID(content),
			Mode:   "100644",
			Size:   info.size,
			Binary: info.binary,
		}
		return encode(map[string]any{"ok": true, "sha": "worktree", "uncommitted": true, "file": file, "content": content}, "could not encode file")
	}

	worktreeFiles, worktreeArchive, ok := baseArchive.worktreeFiles()
	if !ok {
		return failure("worktree is invalid")
	}

	if operation == "tree" {
		listed := make([]gitFile, 0, len(worktreeFiles))
		for _, file := range worktreeFiles {
			listed = append(listed, file)
		}
		sort.Slice(listed, func(i, j int) bool {
			return listed[i].Path < listed[j].Path
		})
		synthetic := gitCommit{Parents: []string{headSHA}, Message: "Uncommitted changes"}
		return encode(map[string]any{"ok": true, "sha": "worktree", "uncommitted": true, "commit": synthetic, "files": listed}, "could not encode worktree tree")
	}

	parentFiles, ok := treeFiles(baseArchive, headCommit.Tree)
	if !ok {
		return failure("commit tree is invalid")
	}
	changes, additions, deletions, legacy := diffFileMaps(parentFiles, worktreeFiles)
	if len(changes) == 0 {
		if operation == "summary" {
			return encode(map[string]any{"ok": true, "dirty": false}, "could not encode worktree summary")
		}
		return failure("no uncommitted changes in this workspace")
	}

	synthetic := gitCommit{Parents: []string{headSHA}, Message: "Uncommitted changes"}

	switch operation {
	case "summary":
		targets, others, totals := structuredChanges(changes, baseArchive, parentFiles, worktreeArchive, worktreeFiles)
		computed := commitChanges{
			legacy: legacy, targets: targets, others: others, totals: totals,
			spriteDirs: spriteDirsOf(parentFiles, worktreeFiles),
		}
		summary := summarizeComputed(computed)
		summary["ok"] = true
		return encode(map[string]any{
			"ok": true, "dirty": true, "head": headSHA, "changedFiles": len(changes), "summary": summary,
		}, "could not encode worktree summary")
	case "changes":
		targets, others, totals := structuredChanges(changes, baseArchive, parentFiles, worktreeArchive, worktreeFiles)
		inlineDiffContent(others, baseArchive, parentFiles, worktreeArchive, worktreeFiles)
		return encode(map[string]any{
			"ok": true, "sha": "worktree", "uncommitted": true, "parent": headSHA, "commit": synthetic,
			"legacy": legacy, "targets": targets, "files": others, "totals": totals,
		}, "could not encode worktree changes")
	case "commit-inline":
		inlineDiffContent(changes, baseArchive, parentFiles, worktreeArchive, worktreeFiles)
		return encode(map[string]any{
			"ok": true, "sha": "worktree", "uncommitted": true, "parent": headSHA, "parentTree": headCommit.Tree,
			"commit": synthetic, "files": changes, "additions": additions, "deletions": deletions, "legacy": legacy,
		}, "could not encode worktree inspection")
	default:
		return encode(map[string]any{
			"ok": true, "sha": "worktree", "uncommitted": true, "parent": headSHA, "parentTree": headCommit.Tree,
			"commit": synthetic, "files": changes, "additions": additions, "deletions": deletions, "legacy": legacy,
		}, "could not encode worktree inspection")
	}
}
