package gitinspection

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	"mistwarp.local/gitinspection/rewrite"
)

// Resolve refs independently of client metadata. Ambiguous entries and packed
// history are rejected; callers return a recoverable conflict.
func readHistoryArchive(path string) (*zip.ReadCloser, archive, map[string]*zip.File, map[string]any, map[string]string, string, error) {
	r, a, err := openArchive(path)
	if err != nil {
		return nil, a, nil, nil, nil, "", err
	}
	fail := func(e string) (*zip.ReadCloser, archive, map[string]*zip.File, map[string]any, map[string]string, string, error) {
		r.Close()
		return nil, a, nil, nil, nil, "", fmt.Errorf("%s", e)
	}
	files := map[string]*zip.File{}
	refs := map[string]string{}
	m := map[string]any{}
	head := ""
	read := func(f *zip.File) ([]byte, error) {
		if f.UncompressedSize64 > maxObjectBytes {
			return nil, fmt.Errorf("entry too large")
		}
		s, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer s.Close()
		return io.ReadAll(io.LimitReader(s, maxObjectBytes+1))
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if files[f.Name] != nil {
			return fail("duplicate archive entry")
		}
		files[f.Name] = f
		if f.Name == ".git/packed-refs" || strings.HasPrefix(f.Name, ".git/objects/pack/") {
			return fail("expand packed history before uploading")
		}
		if f.Name == "mwp.json" {
			b, e := read(f)
			if e != nil || json.Unmarshal(b, &m) != nil {
				return fail("invalid manifest")
			}
		}
		if f.Name == ".git/HEAD" {
			b, e := read(f)
			if e != nil {
				return fail("invalid HEAD")
			}
			head = strings.TrimSpace(string(b))
		}
		if strings.HasPrefix(f.Name, ".git/refs/") {
			if !strings.HasPrefix(f.Name, ".git/refs/heads/") {
				return fail("unsupported history ref")
			}
			branch := strings.TrimPrefix(f.Name, ".git/refs/heads/")
			b, e := read(f)
			oid := strings.TrimSpace(string(b))
			if e != nil || !validBranchName(branch) || !validRestoreOID(oid) {
				return fail("invalid branch ref")
			}
			refs[branch] = oid
		}
	}
	branch := strings.TrimPrefix(head, "ref: refs/heads/")
	if !strings.HasPrefix(head, "ref: refs/heads/") || refs[branch] == "" {
		return fail("HEAD must resolve to a branch")
	}
	if m["head"] != refs[branch] || m["branch"] != branch {
		return fail("manifest disagrees with HEAD/refs")
	}
	return r, a, files, m, refs, branch, nil
}
func repositoryHistory(a archive, refs map[string]string, branch string) ([]any, map[string]any, error) {
	commits := []any{}
	nodes := []any{}
	logs := []any{}
	branches := []string{branch}
	for b := range refs {
		if b != branch {
			branches = append(branches, b)
		}
	}
	sort.Strings(branches[1:])
	seen := map[string]map[string]any{}
	trees := map[string]bool{}
	for _, b := range branches {
		queue := []string{refs[b]}
		visited := map[string]bool{}
		oids := []string{}
		for len(queue) > 0 {
			oid := queue[0]
			queue = queue[1:]
			if visited[oid] {
				continue
			}
			visited[oid] = true
			if len(visited) > maxReachable {
				return nil, nil, fmt.Errorf("history exceeds validation limit")
			}
			c, ok := a.parseCommit(oid)
			if !ok {
				return nil, nil, fmt.Errorf("missing or invalid commit %s", oid)
			}
			if !trees[c.Tree] {
				if _, ok := a.walkTree(c.Tree); !ok {
					return nil, nil, fmt.Errorf("missing or invalid tree/blob for %s", oid)
				}
				trees[c.Tree] = true
			}
			if len(oids) < 200 {
				oids = append(oids, oid)
			}
			if b == branch && len(commits) < 50 {
				commits = append(commits, map[string]any{"sha": oid, "message": c.Message, "author": c.Author, "date": c.Date})
			}
			if seen[oid] == nil && len(nodes) < 200 {
				node := map[string]any{"sha": oid, "message": c.Message, "author": c.Author, "date": c.Date, "parents": c.Parents, "branches": []string{}}
				nodes = append(nodes, node)
				seen[oid] = node
			}
			if node := seen[oid]; node != nil && oid == refs[b] {
				node["branches"] = append(node["branches"].([]string), b)
			}
			queue = append(queue, c.Parents...)
		}
		logs = append(logs, map[string]any{"branch": b, "oids": oids})
	}
	return commits, map[string]any{"branches": branches, "branchLogs": logs, "nodes": nodes}, nil
}

// Full archives must be independently complete. Deltas are validated against
// their declared materialized base. Both emit a canonical full archive, so
// publication never relies on stale client graphs or drops required layers.
func ValidateHistoryArchive(basePath, incomingPath, outputPath, currentHead, forkBase, clientJSON string, identity ...string) restoreResult {
	r, a, files, m, refs, branch, err := readHistoryArchive(incomingPath)
	if err != nil {
		return restoreResult{Error: err.Error()}
	}
	defer r.Close()
	client := map[string]any{}
	if clientJSON != "" {
		if json.Unmarshal([]byte(clientJSON), &client) != nil {
			return restoreResult{Error: "invalid history metadata"}
		}
		for _, k := range []string{"head", "branch", "delta", "baseHead"} {
			if !reflect.DeepEqual(client[k], m[k]) {
				return restoreResult{Error: "client metadata disagrees with archive: " + k}
			}
		}
	}
	delta, _ := m["delta"].(bool)
	oldRefs := map[string]string{}
	if basePath != "" {
		br, _, bf, _, brefs, _, e := readHistoryArchive(basePath)
		if e != nil {
			return restoreResult{Error: "stored history: " + e.Error()}
		}
		defer br.Close()
		oldRefs = brefs
		if delta {
			for n, f := range bf {
				if strings.HasPrefix(n, ".git/objects/") && files[n] == nil {
					files[n] = f
					a.objects[n] = f
				}
			}
			for b, h := range brefs {
				if refs[b] == "" {
					refs[b] = h
					files[".git/refs/heads/"+b] = bf[".git/refs/heads/"+b]
				}
			}
		}
	}
	if delta && (basePath == "" || currentHead == "" || m["baseHead"] != currentHead) {
		return restoreResult{Error: "delta base changed"}
	}
	commits, graph, e := repositoryHistory(a, refs, branch)
	if e != nil {
		return restoreResult{Error: e.Error()}
	}
	head := refs[branch]
	if currentHead != "" && !a.reachable(currentHead, head) {
		return restoreResult{Error: "save does not descend from stored HEAD"}
	}
	if forkBase != "" && !a.reachable(forkBase, head) {
		return restoreResult{Error: "save does not preserve recorded fork base"}
	}
	heads := []string{}
	for _, h := range refs {
		heads = append(heads, h)
	}
	for _, h := range oldRefs {
		if !a.reachable(h, strings.Join(heads, ",")) {
			return restoreResult{Error: "save discards stored branch history"}
		}
	}
	m["commits"] = commits
	m["graph"] = graph
	m["head"] = head
	m["branch"] = branch
	m["delta"] = false
	m["baseHead"] = nil
	m["baseCommit"] = nil
	if forkBase != "" {
		m["baseCommit"] = forkBase
	}
	if len(identity) == 2 {
		m["projectId"] = identity[0]
		m["remixParent"] = nil
		if identity[1] != "" {
			m["remixParent"] = identity[1]
		}
	}
	if err := writeValidatedArchive(outputPath, files, m); err != nil {
		return restoreResult{Error: err.Error()}
	}
	return restoreResult{OK: true, Head: head, Manifest: m}
}
func writeValidatedArchive(path string, files map[string]*zip.File, m map[string]any) (err error) {
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
	for n, f := range files {
		if n == "mwp.json" || n == ".git/shallow" || n == ".git/HEAD" {
			continue
		}
		s, e := f.Open()
		if e != nil {
			return e
		}
		d, e := w.CreateHeader(&f.FileHeader)
		if e == nil {
			_, e = io.Copy(d, s)
		}
		s.Close()
		if e != nil {
			return e
		}
	}
	h, err := w.Create(".git/HEAD")
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(h, "ref: refs/heads/%s\n", m["branch"]); err != nil {
		return err
	}
	d, err := w.Create("mwp.json")
	if err != nil {
		return err
	}
	return json.NewEncoder(d).Encode(m)
}
func ValidateHistoryArchiveJSON(basePath, incomingPath, outputPath, currentHead, forkBase, clientJSON string, identity ...string) string {
	b, _ := json.Marshal(ValidateHistoryArchive(basePath, incomingPath, outputPath, currentHead, forkBase, clientJSON, identity...))
	return string(b)
}
func GenerateHistoryArchiveJSON(projectJSONPath, assetsPath, outputPath, projectID, owner string, created float64, parentPath, parentHead string, parentID ...string) string {
	temporary := outputPath + ".generated"
	defer os.Remove(temporary)
	if rewrite.WriteGeneratedHistoryArchiveJSON(projectJSONPath, assetsPath, temporary, projectID, owner, created, parentPath, parentHead) == "" {
		return failure("could not generate history")
	}
	parent := ""
	if len(parentID) > 0 {
		parent = parentID[0]
	}
	return ValidateHistoryArchiveJSON("", temporary, outputPath, "", parentHead, "", projectID, parent)
}

// ForkHistoryArchive resolves the requested branch from actual refs and emits
// a compact archive. The editor checks out that tree rather than a stale worktree.
func ForkHistoryArchive(sourcePath, outputPath, branch, projectID, parentID string) restoreResult {
	r, a, files, m, refs, _, err := readHistoryArchive(sourcePath)
	if err != nil {
		return restoreResult{Error: err.Error()}
	}
	defer r.Close()
	head := refs[branch]
	if head == "" {
		return restoreResult{Error: "requested branch does not exist"}
	}
	commits, graph, err := repositoryHistory(a, refs, branch)
	if err != nil {
		return restoreResult{Error: err.Error()}
	}
	m["head"] = head
	m["branch"] = branch
	m["commits"] = commits
	m["graph"] = graph
	m["projectId"] = projectID
	m["remixParent"] = parentID
	m["baseCommit"] = head
	m["delta"] = false
	m["baseHead"] = nil
	m["worktree"] = false
	for n := range files {
		if !strings.HasPrefix(n, ".git/") {
			delete(files, n)
		}
	}
	if err := writeValidatedArchive(outputPath, files, m); err != nil {
		return restoreResult{Error: err.Error()}
	}
	return restoreResult{OK: true, Head: head, Manifest: m}
}
func ForkHistoryArchiveJSON(sourcePath, outputPath, branch, projectID, parentID string) string {
	b, _ := json.Marshal(ForkHistoryArchive(sourcePath, outputPath, branch, projectID, parentID))
	return string(b)
}
