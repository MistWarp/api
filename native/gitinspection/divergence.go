package gitinspection

func (a archive) reachableSet(head string) (map[string]bool, bool) {
	if len(head) != 40 {
		return nil, false
	}
	if _, ok := a.parseCommit(head); !ok {
		return nil, false
	}
	visited := make(map[string]bool)
	queue := []string{head}
	for len(queue) > 0 {
		oid := queue[0]
		queue = queue[1:]
		if visited[oid] {
			continue
		}
		if len(visited) >= maxReachable {
			return nil, false
		}
		visited[oid] = true
		commit, ok := a.parseCommit(oid)
		if !ok {
			continue
		}
		queue = append(queue, commit.Parents...)
	}
	return visited, true
}

func CompareDivergence(childPath, childHead, parentPath, parentHead string) string {
	if childHead == "" || parentHead == "" {
		return encode(map[string]any{"ok": true, "commonAncestor": false}, "could not encode divergence")
	}
	if childHead == parentHead {
		return encode(map[string]any{"ok": true, "commonAncestor": true, "ahead": 0, "behind": 0}, "could not encode divergence")
	}
	releaseSlot := acquireInspectionSlot()
	defer releaseSlot()

	childShared, err := acquireSharedArchive(childPath)
	if err != nil {
		return failure("child workspace archive is unavailable")
	}
	defer childShared.release()

	parentShared, err := acquireSharedArchive(parentPath)
	if err != nil {
		return failure("parent workspace archive is unavailable")
	}
	defer parentShared.release()

	childSet, ok := childShared.archive.reachableSet(childHead)
	if !ok {
		return encode(map[string]any{"ok": true, "commonAncestor": false}, "could not encode divergence")
	}
	parentSet, ok := parentShared.archive.reachableSet(parentHead)
	if !ok {
		return encode(map[string]any{"ok": true, "commonAncestor": false}, "could not encode divergence")
	}

	shared := false
	ahead := 0
	for sha := range childSet {
		if parentSet[sha] {
			shared = true
		} else {
			ahead++
		}
	}
	if !shared {
		return encode(map[string]any{"ok": true, "commonAncestor": false}, "could not encode divergence")
	}
	behind := 0
	for sha := range parentSet {
		if !childSet[sha] {
			behind++
		}
	}
	return encode(map[string]any{
		"ok": true, "commonAncestor": true, "ahead": ahead, "behind": behind,
	}, "could not encode divergence")
}
