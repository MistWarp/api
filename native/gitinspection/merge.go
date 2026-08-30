package gitinspection

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"

	"github.com/epiclabs-io/diff3"
)

type mergeChange struct {
	Path          string `json:"path"`
	Deleted       bool   `json:"deleted,omitempty"`
	Binary        bool   `json:"binary,omitempty"`
	Conflict      bool   `json:"conflict,omitempty"`
	Content       []byte `json:"content,omitempty"`
	Ours          []byte `json:"ours,omitempty"`
	Theirs        []byte `json:"theirs,omitempty"`
	OursDeleted   bool   `json:"oursDeleted,omitempty"`
	TheirsDeleted bool   `json:"theirsDeleted,omitempty"`
}

type prepareMergeResult struct {
	OK            bool          `json:"ok"`
	Error         string        `json:"error,omitempty"`
	BaseHead      string        `json:"baseHead,omitempty"`
	AlreadyMerged bool          `json:"alreadyMerged,omitempty"`
	Changes       []mergeChange `json:"changes,omitempty"`
	Conflicts     []string      `json:"conflicts,omitempty"`
}

func ancestorDistances(repository archive, head string) map[string]int {
	distances := map[string]int{}
	type queuedCommit struct {
		oid      string
		distance int
	}
	queue := []queuedCommit{{oid: head}}
	for len(queue) > 0 && len(distances) <= maxReachable {
		candidate := queue[0]
		queue = queue[1:]
		if previous, seen := distances[candidate.oid]; seen && previous <= candidate.distance {
			continue
		}
		commit, ok := repository.parseCommit(candidate.oid)
		if !ok {
			continue
		}
		distances[candidate.oid] = candidate.distance
		for _, parent := range commit.Parents {
			queue = append(queue, queuedCommit{oid: parent, distance: candidate.distance + 1})
		}
	}
	return distances
}

func mergeBase(repository archive, targetHead, sourceHead string) (string, bool) {
	targetAncestors := ancestorDistances(repository, targetHead)
	sourceAncestors := ancestorDistances(repository, sourceHead)
	best := ""
	bestDistance := int(^uint(0) >> 1)
	for oid, targetDistance := range targetAncestors {
		if sourceDistance, common := sourceAncestors[oid]; common && targetDistance+sourceDistance < bestDistance {
			best = oid
			bestDistance = targetDistance + sourceDistance
		}
	}
	return best, best != ""
}

func filesAtCommit(repository archive, sha string) (map[string]gitFile, bool) {
	commit, ok := repository.parseCommit(sha)
	if !ok {
		return nil, false
	}
	files, ok := repository.walkTree(commit.Tree)
	if !ok {
		return nil, false
	}
	result := make(map[string]gitFile, len(files))
	for _, file := range files {
		result[file.Path] = file
	}
	return result, true
}

func sameFile(left, right gitFile, leftOK, rightOK bool) bool {
	return leftOK == rightOK && (!leftOK || left.OID == right.OID)
}

func fileBytes(repository archive, file gitFile, exists bool) ([]byte, bool) {
	if !exists {
		return nil, true
	}
	kind, content, ok := repository.readObject(file.OID)
	return content, ok && kind == "blob"
}

// PrepareMerge performs the Git three-way content merge without writing a
// commit. It returns only paths that differ from the target tree.
func PrepareMerge(targetPath, targetHead, sourcePath, sourceHead, baseHead string) prepareMergeResult {
	targetReader, target, err := openArchive(targetPath)
	if err != nil {
		return prepareMergeResult{Error: "target workspace archive is unavailable"}
	}
	defer targetReader.Close()
	sourceReader, source, err := openArchive(sourcePath)
	if err != nil {
		return prepareMergeResult{Error: "source workspace archive is unavailable"}
	}
	defer sourceReader.Close()
	baseFiles, ok := filesAtCommit(target, baseHead)
	if !ok {
		return prepareMergeResult{Error: "pull request base is unavailable"}
	}
	oursFiles, ok := filesAtCommit(target, targetHead)
	if !ok {
		return prepareMergeResult{Error: "target head is unavailable"}
	}
	theirsFiles, ok := filesAtCommit(source, sourceHead)
	if !ok || !source.reachable(baseHead, sourceHead) {
		return prepareMergeResult{Error: "source head does not descend from the pull request base"}
	}
	paths := make(map[string]bool)
	for path := range baseFiles {
		paths[path] = true
	}
	for path := range oursFiles {
		paths[path] = true
	}
	for path := range theirsFiles {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	result := prepareMergeResult{OK: true, Changes: []mergeChange{}, Conflicts: []string{}}
	for _, path := range ordered {
		baseFile, baseOK := baseFiles[path]
		oursFile, oursOK := oursFiles[path]
		theirsFile, theirsOK := theirsFiles[path]
		if sameFile(oursFile, theirsFile, oursOK, theirsOK) || sameFile(theirsFile, baseFile, theirsOK, baseOK) {
			continue
		}
		if sameFile(oursFile, baseFile, oursOK, baseOK) {
			content, valid := fileBytes(source, theirsFile, theirsOK)
			if !valid {
				return prepareMergeResult{Error: "source file object is unavailable"}
			}
			result.Changes = append(result.Changes, mergeChange{Path: path, Deleted: !theirsOK, Binary: theirsFile.Binary, Content: content})
			continue
		}
		baseContent, baseValid := fileBytes(target, baseFile, baseOK)
		oursContent, oursValid := fileBytes(target, oursFile, oursOK)
		theirsContent, theirsValid := fileBytes(source, theirsFile, theirsOK)
		if !baseValid || !oursValid || !theirsValid {
			return prepareMergeResult{Error: "merge file object is unavailable"}
		}
		binary := (baseOK && baseFile.Binary) || (oursOK && oursFile.Binary) || (theirsOK && theirsFile.Binary)
		if binary || !oursOK || !theirsOK {
			result.Changes = append(result.Changes, mergeChange{
				Path: path, Binary: true, Conflict: true, Ours: oursContent, Theirs: theirsContent,
				OursDeleted: !oursOK, TheirsDeleted: !theirsOK,
			})
			result.Conflicts = append(result.Conflicts, path)
			continue
		}
		merged, mergeErr := diff3.Merge(bytes.NewReader(oursContent), bytes.NewReader(baseContent), bytes.NewReader(theirsContent), true, "target", "pull request")
		if mergeErr != nil {
			return prepareMergeResult{Error: "could not merge text file"}
		}
		content, readErr := io.ReadAll(merged.Result)
		if readErr != nil {
			return prepareMergeResult{Error: "could not read merged text file"}
		}
		result.Changes = append(result.Changes, mergeChange{Path: path, Content: content, Conflict: merged.Conflicts})
		if merged.Conflicts {
			result.Conflicts = append(result.Conflicts, path)
		}
	}
	return result
}

func PrepareMergeJSON(targetPath, targetHead, sourcePath, sourceHead, baseHead string) string {
	encoded, _ := json.Marshal(PrepareMerge(targetPath, targetHead, sourcePath, sourceHead, baseHead))
	return string(encoded)
}

// PrepareBranchMerge finds the common ancestor of two branch tips in one
// repository and prepares a regular three-way merge against it.
func PrepareBranchMerge(path, targetHead, sourceHead string) prepareMergeResult {
	reader, repository, err := openArchive(path)
	if err != nil {
		return prepareMergeResult{Error: "project workspace archive is unavailable"}
	}
	defer reader.Close()
	baseHead, ok := mergeBase(repository, targetHead, sourceHead)
	if !ok {
		return prepareMergeResult{Error: "the branches do not share a common commit"}
	}
	if baseHead == sourceHead {
		return prepareMergeResult{OK: true, BaseHead: baseHead, AlreadyMerged: true, Changes: []mergeChange{}, Conflicts: []string{}}
	}
	result := PrepareMerge(path, targetHead, path, sourceHead, baseHead)
	result.BaseHead = baseHead
	return result
}

func PrepareBranchMergeJSON(path, targetHead, sourceHead string) string {
	encoded, _ := json.Marshal(PrepareBranchMerge(path, targetHead, sourceHead))
	return string(encoded)
}
