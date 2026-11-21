package gitinspection

import (
	"strings"
)

const maxLastCommitWalk = 2000

var maxLastCommitParseBytes = 64 << 20

type treeEntry struct {
	OID string
	Dir bool
}

func (a archive) readTreeEntries(oid string) (map[string]treeEntry, bool) {
	body, ok := a.treeObject(oid)
	if !ok {
		return nil, false
	}
	items, ok := parseTreeEntries(body)
	if !ok {
		return nil, false
	}
	entries := make(map[string]treeEntry, len(items))
	for _, item := range items {
		entries[item.name] = treeEntry{OID: item.oid, Dir: treeMode(item.mode)}
	}
	return entries, true
}

func (a archive) headPaths(rootOID string) (map[string]bool, bool) {
	paths := map[string]bool{}
	var walk func(string, string, int) bool
	walk = func(treeOID, prefix string, depth int) bool {
		if depth > maxTreeDepth {
			return false
		}
		entries, ok := a.readTreeEntries(treeOID)
		if !ok {
			return false
		}
		for name, entry := range entries {
			path := joinTreePath(prefix, name)
			paths[path] = true
			if len(paths) > maxTreeFiles {
				return false
			}
			if entry.Dir && !walk(entry.OID, path, depth+1) {
				return false
			}
		}
		return true
	}
	return paths, walk(rootOID, "", 0)
}

func joinTreePath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

type lastCommitWalk struct {
	archive   archive
	wanted    map[string]bool
	budget    int
	exhausted bool
	visit     func(string)
}

func (w *lastCommitWalk) fractchChanged(beforeOID, afterOID string) bool {
	_, before, beforeOK := w.archive.readObject(beforeOID)
	_, after, afterOK := w.archive.readObject(afterOID)
	if !beforeOK || !afterOK || len(before) > maxInlineDiffFileBytes || len(after) > maxInlineDiffFileBytes {
		return true
	}
	if w.budget+len(before)+len(after) > maxLastCommitParseBytes {
		w.exhausted = true
		return false
	}
	w.budget += len(before) + len(after)
	changes := compareFractch(string(before), string(after))
	return len(changes.Scripts) > 0 || len(changes.Items) > 0
}

func (w *lastCommitWalk) changedPaths(currentOID, parentOID, prefix string, depth int) (bool, bool) {
	if depth > maxTreeDepth {
		return false, false
	}
	current, ok := w.archive.readTreeEntries(currentOID)
	if !ok {
		return false, false
	}
	parent := map[string]treeEntry{}
	if parentOID != "" {
		if parent, ok = w.archive.readTreeEntries(parentOID); !ok {
			return false, false
		}
	}
	changed := false
	for name, entry := range current {
		path := joinTreePath(prefix, name)
		before, existed := parent[name]
		if existed && before.OID == entry.OID {
			continue
		}
		counted := true
		if entry.Dir {
			previous := ""
			if existed && before.Dir {
				previous = before.OID
			}
			nested, ok := w.changedPaths(entry.OID, previous, path, depth+1)
			if !ok {
				return false, false
			}
			counted = nested || previous == ""
		} else if existed && !before.Dir && strings.HasSuffix(strings.ToLower(name), ".fractch") {
			counted = w.fractchChanged(before.OID, entry.OID)
		}
		if !counted {
			continue
		}
		changed = true
		if w.wanted[path] {
			w.visit(path)
		}
	}
	for name := range parent {
		if _, kept := current[name]; !kept {
			changed = true
		}
	}
	return changed, true
}

func (a archive) inspectLastCommits(sha string, commit gitCommit) string {
	wanted, ok := a.headPaths(commit.Tree)
	if !ok {
		return failure("commit tree is invalid")
	}
	paths := map[string]string{}
	commits := map[string]any{}
	remaining := len(wanted)
	current, currentCommit := sha, commit
	complete := true
	walk := &lastCommitWalk{archive: a, wanted: wanted}
	for walked := 0; remaining > 0; walked++ {
		if walked >= maxLastCommitWalk {
			complete = false
			break
		}
		parentTree := ""
		var parentCommit gitCommit
		parentSHA := ""
		if len(currentCommit.Parents) > 0 {
			parentSHA = currentCommit.Parents[0]
			parsed, found := a.parseCommit(parentSHA)
			if !found {
				complete = false
				break
			}
			parentCommit = parsed
			parentTree = parsed.Tree
		}
		used := false
		walk.visit = func(path string) {
			if _, done := paths[path]; done {
				return
			}
			paths[path] = current
			remaining--
			used = true
		}
		if _, ok := walk.changedPaths(currentCommit.Tree, parentTree, "", 0); !ok {
			return failure("commit tree is invalid")
		}
		if walk.exhausted {
			complete = false
			for path, owner := range paths {
				if owner == current {
					delete(paths, path)
				}
			}
			break
		}
		if used {
			commits[current] = map[string]any{
				"author": currentCommit.Author, "date": currentCommit.Date, "message": currentCommit.Message,
			}
		}
		if parentSHA == "" {
			break
		}
		current, currentCommit = parentSHA, parentCommit
	}
	return encode(map[string]any{
		"ok": true, "sha": sha, "paths": paths, "commits": commits, "complete": complete && remaining == 0,
	}, "could not encode last commits")
}
