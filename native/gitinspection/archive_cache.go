package gitinspection

import (
	"archive/zip"
	"os"
	"runtime"
	"sync"
	"time"
)

const (
	sharedArchiveLimit   = 6
	sharedArchiveIdle    = 5 * time.Minute
	memoCommitLimit      = 20_000
	memoBlobLimit        = 100_000
	memoTreeBytesLimit   = 16 << 20
	memoListingFileLimit = 100_000
	memoReachLimit       = 16
)

type blobInfo struct {
	size   int
	binary bool
}

type reachSet struct {
	once    sync.Once
	visited map[string]bool
}

type objectMemo struct {
	mu           sync.Mutex
	commits      map[string]gitCommit
	blobs        map[string]blobInfo
	trees        map[string][]byte
	treeBytes    int
	listings     map[string][]gitFile
	listingFiles int
	reach        map[string]*reachSet
}

func newObjectMemo() *objectMemo {
	return &objectMemo{
		commits:  make(map[string]gitCommit),
		blobs:    make(map[string]blobInfo),
		trees:    make(map[string][]byte),
		listings: make(map[string][]gitFile),
		reach:    make(map[string]*reachSet),
	}
}

func cloneCommit(commit gitCommit) gitCommit {
	commit.Parents = append([]string{}, commit.Parents...)
	return commit
}

func (m *objectMemo) commit(oid string) (gitCommit, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	commit, ok := m.commits[oid]
	if !ok {
		return gitCommit{}, false
	}
	return cloneCommit(commit), true
}

func (m *objectMemo) storeCommit(oid string, commit gitCommit) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.commits) < memoCommitLimit {
		m.commits[oid] = cloneCommit(commit)
	}
}

func (m *objectMemo) blob(oid string) (blobInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.blobs[oid]
	return info, ok
}

func (m *objectMemo) storeBlob(oid string, info blobInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.blobs) < memoBlobLimit {
		m.blobs[oid] = info
	}
}

func (m *objectMemo) tree(oid string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	body, ok := m.trees[oid]
	return body, ok
}

func (m *objectMemo) storeTree(oid string, body []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.trees[oid]; exists || m.treeBytes+len(body) > memoTreeBytesLimit {
		return
	}
	m.trees[oid] = body
	m.treeBytes += len(body)
}

func (m *objectMemo) listing(treeOID string) ([]gitFile, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	files, ok := m.listings[treeOID]
	if !ok {
		return nil, false
	}
	return append([]gitFile{}, files...), true
}

func (m *objectMemo) storeListing(treeOID string, files []gitFile) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.listings[treeOID]; exists || m.listingFiles+len(files) > memoListingFileLimit {
		return
	}
	m.listings[treeOID] = append([]gitFile{}, files...)
	m.listingFiles += len(files)
}

func (m *objectMemo) reachableFrom(allowedHeads string, walk func() map[string]bool) (map[string]bool, bool) {
	m.mu.Lock()
	set := m.reach[allowedHeads]
	if set == nil {
		if len(m.reach) >= memoReachLimit {
			m.mu.Unlock()
			return nil, false
		}
		set = &reachSet{}
		m.reach[allowedHeads] = set
	}
	m.mu.Unlock()
	set.once.Do(func() { set.visited = walk() })
	return set.visited, true
}

type sharedArchive struct {
	path     string
	file     *os.File
	info     os.FileInfo
	archive  archive
	ready    chan struct{}
	err      error
	refs     int
	evicted  bool
	lastUsed time.Time
}

var sharedArchives = struct {
	sync.Mutex
	entries map[string]*sharedArchive
}{entries: make(map[string]*sharedArchive)}

var sharedArchiveJanitor sync.Once

var inspectionSlots = make(chan struct{}, max(2, runtime.NumCPU()/2))

func acquireInspectionSlot() func() {
	inspectionSlots <- struct{}{}
	return func() { <-inspectionSlots }
}

func sameArchiveFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (s *sharedArchive) closeLocked() {
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
}

func dropSharedArchiveLocked(entry *sharedArchive) {
	if sharedArchives.entries[entry.path] == entry {
		delete(sharedArchives.entries, entry.path)
	}
	entry.evicted = true
	if entry.refs == 0 {
		entry.closeLocked()
	}
}

func evictSharedArchivesLocked(now time.Time) {
	for _, entry := range sharedArchives.entries {
		if entry.refs == 0 && now.Sub(entry.lastUsed) > sharedArchiveIdle {
			dropSharedArchiveLocked(entry)
		}
	}
	for len(sharedArchives.entries) > sharedArchiveLimit {
		var oldest *sharedArchive
		for _, entry := range sharedArchives.entries {
			if oldest == nil || entry.lastUsed.Before(oldest.lastUsed) {
				oldest = entry
			}
		}
		dropSharedArchiveLocked(oldest)
	}
}

func (s *sharedArchive) release() {
	sharedArchives.Lock()
	defer sharedArchives.Unlock()
	s.refs--
	s.lastUsed = time.Now()
	if s.evicted && s.refs == 0 {
		s.closeLocked()
	}
}

func openSharedArchiveFile(path string) (*os.File, os.FileInfo, archive, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, archive{}, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, archive{}, err
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		file.Close()
		return nil, nil, archive{}, err
	}
	opened := indexArchive(reader.File)
	opened.memo = newObjectMemo()
	return file, info, opened, nil
}

func startSharedArchiveJanitor() {
	go func() {
		for range time.Tick(time.Minute) {
			sharedArchives.Lock()
			evictSharedArchivesLocked(time.Now())
			sharedArchives.Unlock()
		}
	}()
}

func acquireSharedArchive(path string) (*sharedArchive, error) {
	sharedArchiveJanitor.Do(startSharedArchiveJanitor)
	info, err := os.Stat(path)
	if err != nil {
		sharedArchives.Lock()
		if entry := sharedArchives.entries[path]; entry != nil {
			dropSharedArchiveLocked(entry)
		}
		sharedArchives.Unlock()
		return nil, err
	}
	now := time.Now()
	sharedArchives.Lock()
	entry := sharedArchives.entries[path]
	if entry != nil && !sameArchiveFile(entry.info, info) {
		dropSharedArchiveLocked(entry)
		entry = nil
	}
	opener := entry == nil
	if opener {
		entry = &sharedArchive{path: path, info: info, ready: make(chan struct{})}
		sharedArchives.entries[path] = entry
	}
	entry.refs++
	entry.lastUsed = now
	evictSharedArchivesLocked(now)
	sharedArchives.Unlock()

	if opener {
		file, openedInfo, opened, openErr := openSharedArchiveFile(path)
		sharedArchives.Lock()
		entry.err = openErr
		if openErr != nil {
			dropSharedArchiveLocked(entry)
		} else {
			entry.file = file
			entry.info = openedInfo
			entry.archive = opened
			if entry.evicted && entry.refs == 0 {
				entry.closeLocked()
			}
		}
		close(entry.ready)
		sharedArchives.Unlock()
	}
	<-entry.ready
	if entry.err != nil {
		entry.release()
		return nil, entry.err
	}
	return entry, nil
}
