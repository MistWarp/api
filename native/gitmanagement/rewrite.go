package gitmanagement

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxObjectBytes = 256 << 20

type result struct {
	OK        bool              `json:"ok"`
	Error     string            `json:"error,omitempty"`
	Head      string            `json:"head,omitempty"`
	Rewritten map[string]string `json:"rewritten,omitempty"`
}

type archive struct{ objects map[string]*zip.File }

func encoded(value result) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func (a archive) readObject(oid string) (string, []byte, bool) {
	if len(oid) != 40 {
		return "", nil, false
	}
	item := a.objects[".git/objects/"+oid[:2]+"/"+oid[2:]]
	if item == nil || item.UncompressedSize64 > maxObjectBytes {
		return "", nil, false
	}
	source, err := item.Open()
	if err != nil {
		return "", nil, false
	}
	compressed, readErr := io.ReadAll(io.LimitReader(source, maxObjectBytes+1))
	closeErr := source.Close()
	if readErr != nil || closeErr != nil {
		return "", nil, false
	}
	inflater, err := zlib.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", nil, false
	}
	objectData, readErr := io.ReadAll(io.LimitReader(inflater, maxObjectBytes+1))
	closeErr = inflater.Close()
	if readErr != nil || closeErr != nil || len(objectData) > maxObjectBytes {
		return "", nil, false
	}
	separator := bytes.IndexByte(objectData, 0)
	if separator < 1 {
		return "", nil, false
	}
	header, body := string(objectData[:separator]), objectData[separator+1:]
	space := strings.IndexByte(header, ' ')
	if space < 1 || header[space+1:] != fmt.Sprint(len(body)) {
		return "", nil, false
	}
	digest := sha1.Sum(objectData)
	if hex.EncodeToString(digest[:]) != oid {
		return "", nil, false
	}
	return header[:space], body, true
}

func commitParts(body []byte) ([]string, string, bool) {
	text := string(body)
	separator := strings.Index(text, "\n\n")
	if separator < 0 {
		return nil, "", false
	}
	return strings.Split(text[:separator], "\n"), text[separator+2:], true
}

func parents(headers []string) []string {
	result := []string{}
	for _, line := range headers {
		if strings.HasPrefix(line, "parent ") {
			result = append(result, strings.TrimPrefix(line, "parent "))
		}
	}
	return result
}

func writeObject(kind string, body []byte) (string, []byte, error) {
	objectData := append([]byte(fmt.Sprintf("%s %d\x00", kind, len(body))), body...)
	digest := sha1.Sum(objectData)
	oid := hex.EncodeToString(digest[:])
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(objectData); err != nil {
		return "", nil, err
	}
	if err := writer.Close(); err != nil {
		return "", nil, err
	}
	return oid, compressed.Bytes(), nil
}

func rewrittenCommit(body []byte, newMessage string, replacementParent string, replaceMessage bool) ([]byte, error) {
	headers, message, ok := commitParts(body)
	if !ok {
		return nil, fmt.Errorf("invalid commit")
	}
	if replacementParent != "" {
		for index, line := range headers {
			if strings.HasPrefix(line, "parent ") {
				headers[index] = "parent " + replacementParent
				break
			}
		}
	}
	if replaceMessage {
		message = newMessage + "\n"
	}
	return []byte(strings.Join(headers, "\n") + "\n\n" + message), nil
}

func validBranch(branch string) bool {
	if branch == "" || len(branch) > 100 || strings.HasPrefix(branch, "/") ||
		strings.HasSuffix(branch, "/") || strings.Contains(branch, "\\") || strings.Contains(branch, "..") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// RewriteFirstParent renames target and rewrites its first-parent descendants to head.
// Trees, author/committer headers, timestamps, and non-first merge parents are preserved.
func RewriteFirstParent(workspacePath, outputPath, target, head, branch, message string) string {
	if !validBranch(branch) {
		return encoded(result{Error: "branch name is invalid"})
	}
	reader, err := zip.OpenReader(workspacePath)
	if err != nil {
		return encoded(result{Error: "workspace archive is unavailable"})
	}
	defer reader.Close()
	objects := map[string]*zip.File{}
	for _, item := range reader.File {
		if strings.HasPrefix(item.Name, ".git/objects/") && !item.FileInfo().IsDir() {
			objects[item.Name] = item
		}
	}
	archive := archive{objects: objects}
	chain := []string{}
	current := head
	found := false
	for len(chain) < 10_000 {
		kind, body, ok := archive.readObject(current)
		if !ok || kind != "commit" {
			return encoded(result{Error: "commit chain is unavailable"})
		}
		chain = append(chain, current)
		if current == target {
			found = true
			break
		}
		headers, _, valid := commitParts(body)
		if !valid {
			return encoded(result{Error: "commit chain is invalid"})
		}
		ps := parents(headers)
		if len(ps) == 0 {
			break
		}
		current = ps[0]
	}
	if !found {
		return encoded(result{Error: "commit is not on the current branch's first-parent history"})
	}

	rewritten := map[string]string{}
	newObjects := map[string][]byte{}
	replacementParent := ""
	for index := len(chain) - 1; index >= 0; index-- {
		oldOID := chain[index]
		_, body, _ := archive.readObject(oldOID)
		newBody, rewriteErr := rewrittenCommit(body, message, replacementParent, oldOID == target)
		if rewriteErr != nil {
			return encoded(result{Error: rewriteErr.Error()})
		}
		newOID, compressed, writeErr := writeObject("commit", newBody)
		if writeErr != nil {
			return encoded(result{Error: "could not encode rewritten commit"})
		}
		rewritten[oldOID] = newOID
		newObjects[newOID] = compressed
		replacementParent = newOID
	}
	newHead := rewritten[head]

	output, err := os.Create(outputPath)
	if err != nil {
		return encoded(result{Error: "could not create rewritten workspace"})
	}
	writer := zip.NewWriter(output)
	manifestName := "mwp.json"
	branchRef := ".git/refs/heads/" + branch
	for _, item := range reader.File {
		if item.Name == manifestName || item.Name == branchRef {
			continue
		}
		source, openErr := item.Open()
		if openErr != nil {
			writer.Close()
			output.Close()
			return encoded(result{Error: "could not read workspace"})
		}
		header := item.FileHeader
		destination, createErr := writer.CreateHeader(&header)
		if createErr == nil {
			_, createErr = io.Copy(destination, source)
		}
		source.Close()
		if createErr != nil {
			writer.Close()
			output.Close()
			return encoded(result{Error: "could not copy workspace"})
		}
	}
	for oid, compressed := range newObjects {
		destination, createErr := writer.Create(".git/objects/" + oid[:2] + "/" + oid[2:])
		if createErr != nil {
			writer.Close()
			output.Close()
			return encoded(result{Error: "could not write commit object"})
		}
		if _, createErr = destination.Write(compressed); createErr != nil {
			writer.Close()
			output.Close()
			return encoded(result{Error: "could not write commit object"})
		}
	}
	ref, err := writer.Create(branchRef)
	if err == nil {
		_, err = ref.Write([]byte(newHead + "\n"))
	}
	if err != nil {
		writer.Close()
		output.Close()
		return encoded(result{Error: "could not update branch"})
	}
	manifest := map[string]any{}
	for _, item := range reader.File {
		if item.Name != manifestName {
			continue
		}
		source, _ := item.Open()
		data, _ := io.ReadAll(source)
		source.Close()
		json.Unmarshal(data, &manifest)
		break
	}
	manifest["head"] = newHead
	manifestData, _ := json.MarshalIndent(manifest, "", "  ")
	manifestFile, err := writer.Create(manifestName)
	if err == nil {
		_, err = manifestFile.Write(manifestData)
	}
	closeErr := writer.Close()
	fileCloseErr := output.Close()
	if err != nil || closeErr != nil || fileCloseErr != nil {
		os.Remove(outputPath)
		return encoded(result{Error: "could not finish rewritten workspace"})
	}
	return encoded(result{OK: true, Head: newHead, Rewritten: rewritten})
}
