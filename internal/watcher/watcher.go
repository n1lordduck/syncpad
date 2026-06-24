package watcher

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/n1lordduck/syncpad/internal/config"
	sftpclient "github.com/n1lordduck/syncpad/internal/sftp"
)

type EventKind int

const (
	KindUpsert EventKind = iota
	KindDelete
)

type PendingFile struct {
	LocalPath string    `json:"local_path"`
	Kind      EventKind `json:"kind"`
}

type Event struct {
	Time    time.Time
	Message string
	Err     bool
}

type debounceEntry struct {
	timer *time.Timer
	op    fsnotify.Op
}

type hashCacheEntry struct {
	Hash  []byte `json:"hash"`
	Mtime int64  `json:"mtime"`
	Size  int64  `json:"size"`
}

type Session struct {
	Container    *config.Container
	GlobalIgnore []string
	Events       chan Event

	mu        sync.Mutex
	pending   map[string]PendingFile
	knownHash map[string][]byte
	hashCache map[string]hashCacheEntry

	pulling atomic.Bool

	stop chan struct{}
	once sync.Once

	pendingFile   string
	hashCacheFile string
}

func NewSession(c *config.Container, globalIgnore []string) *Session {
	pendingFile := ""
	hashCacheFile := ""
	if dir, err := config.PendingDir(); err == nil {
		pendingFile = filepath.Join(dir, c.ID+".json")
		hashCacheFile = filepath.Join(dir, c.ID+".hash.json")
	}

	s := &Session{
		Container:     c,
		GlobalIgnore:  globalIgnore,
		Events:        make(chan Event, 128),
		pending:       make(map[string]PendingFile),
		knownHash:     make(map[string][]byte),
		hashCache:     make(map[string]hashCacheEntry),
		stop:          make(chan struct{}),
		pendingFile:   pendingFile,
		hashCacheFile: hashCacheFile,
	}

	s.pulling.Store(true)
	s.loadHashCache()
	s.loadPending()

	go func() {
		s.buildBaselineHashes()
		s.saveHashCache()
		s.pulling.Store(false)
	}()

	return s
}

func (s *Session) loadHashCache() {
	if s.hashCacheFile == "" {
		return
	}
	data, err := os.ReadFile(s.hashCacheFile)
	if err != nil {
		return
	}
	s.mu.Lock()
	_ = json.Unmarshal(data, &s.hashCache)
	s.mu.Unlock()
}

func (s *Session) saveHashCache() {
	if s.hashCacheFile == "" {
		return
	}
	s.mu.Lock()
	snap := make(map[string]hashCacheEntry, len(s.hashCache))
	for k, v := range s.hashCache {
		snap[k] = v
	}
	s.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return
	}
	_ = os.WriteFile(s.hashCacheFile, data, 0600)
}

func (s *Session) buildBaselineHashes() {
	numWorkers := runtime.NumCPU()
	if numWorkers < 2 {
		numWorkers = 2
	}

	pathsChan := make(chan string, 128)
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range pathsChan {
				info, err := os.Stat(path)
				if err != nil {
					continue
				}
				mtime := info.ModTime().Unix()
				size := info.Size()

				s.mu.Lock()
				entry, ok := s.hashCache[path]
				s.mu.Unlock()

				if ok && entry.Mtime == mtime && entry.Size == size {
					s.mu.Lock()
					s.knownHash[path] = entry.Hash
					s.mu.Unlock()
					continue
				}

				h, err := hashFile(path)
				if err != nil {
					continue
				}

				s.mu.Lock()
				s.knownHash[path] = h
				s.hashCache[path] = hashCacheEntry{Hash: h, Mtime: mtime, Size: size}
				s.mu.Unlock()
			}
		}()
	}

	for _, folder := range s.Container.Folders {
		_ = filepath.WalkDir(folder.LocalPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || s.isIgnored(path) {
				return nil
			}
			pathsChan <- path
			return nil
		})
	}
	close(pathsChan)
	wg.Wait()
}

func (s *Session) loadPending() {
	if s.pendingFile == "" {
		return
	}
	data, err := os.ReadFile(s.pendingFile)
	if err != nil {
		return
	}
	var saved map[string]PendingFile
	if err := json.Unmarshal(data, &saved); err != nil {
		return
	}

	dropped := 0
	for k, v := range saved {
		if v.Kind == KindUpsert {
			if current, err := hashFile(v.LocalPath); err == nil {
				s.mu.Lock()
				known := s.knownHash[v.LocalPath]
				s.mu.Unlock()
				if hashesEqual(current, known) {
					dropped++
					continue
				}
			}
		}
		s.mu.Lock()
		s.pending[k] = v
		s.mu.Unlock()
	}

	s.mu.Lock()
	pendingLen := len(s.pending)
	s.mu.Unlock()

	switch {
	case pendingLen > 0 && dropped > 0:
		s.emit(fmt.Sprintf("Restored %d pending file(s) from last session (%d reverted, skipped).", pendingLen, dropped), false)
	case pendingLen > 0:
		s.emit(fmt.Sprintf("Restored %d pending file(s) from last session.", pendingLen), false)
	case dropped > 0:
		s.emit(fmt.Sprintf("All %d pending file(s) from last session were reverted — nothing to send.", dropped), false)
	}
}

func (s *Session) savePending() {
	if s.pendingFile == "" {
		return
	}
	s.mu.Lock()
	snap := make(map[string]PendingFile, len(s.pending))
	for k, v := range s.pending {
		snap[k] = v
	}
	s.mu.Unlock()

	if len(snap) == 0 {
		_ = os.Remove(s.pendingFile)
		return
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(s.pendingFile, data, 0600)
}

func hashFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

func hashesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Session) isIgnored(path string) bool {
	name := filepath.Base(path)
	allPatterns := make([]string, 0, len(config.DefaultIgnorePatterns)+len(s.GlobalIgnore)+len(s.Container.IgnorePatterns))
	allPatterns = append(allPatterns, config.DefaultIgnorePatterns...)
	allPatterns = append(allPatterns, s.GlobalIgnore...)
	allPatterns = append(allPatterns, s.Container.IgnorePatterns...)
	for _, pattern := range allPatterns {
		matched, err := filepath.Match(pattern, name)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func (s *Session) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func (s *Session) resolveRemotePath(localFilePath string) (string, bool) {
	for _, folder := range s.Container.Folders {
		rel, err := filepath.Rel(folder.LocalPath, localFilePath)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		return filepath.ToSlash(filepath.Join(s.Container.SFTP.RemotePath, folder.Name, rel)), true
	}
	return "", false
}

func (s *Session) Flush() {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	snap := make(map[string]PendingFile, len(s.pending))
	for k, v := range s.pending {
		snap[k] = v
	}
	s.mu.Unlock()

	client, err := sftpclient.Connect(s.Container.SFTP)
	if err != nil {
		s.emit("SFTP connection error: "+err.Error(), true)
		return
	}
	defer client.Close()

	var failed []string
	ok := 0
	for _, pf := range snap {
		remote, found := s.resolveRemotePath(pf.LocalPath)
		if !found {
			s.emit("error: could not resolve remote destination for "+filepath.Base(pf.LocalPath), true)
			failed = append(failed, pf.LocalPath)
			continue
		}

		var opErr error
		var opMsg string
		if pf.Kind == KindUpsert {
			opErr = client.Upload(pf.LocalPath, remote)
			opMsg = "↑ " + filepath.Base(pf.LocalPath)
		} else {
			opErr = client.Delete(remote)
			opMsg = "✕ " + filepath.Base(pf.LocalPath)
		}

		if opErr != nil {
			s.emit("error "+filepath.Base(pf.LocalPath)+": "+opErr.Error(), true)
			failed = append(failed, pf.LocalPath)
			continue
		}

		if pf.Kind == KindUpsert {
			if h, err := hashFile(pf.LocalPath); err == nil {
				info, statErr := os.Stat(pf.LocalPath)
				s.mu.Lock()
				s.knownHash[pf.LocalPath] = h
				if statErr == nil {
					s.hashCache[pf.LocalPath] = hashCacheEntry{
						Hash:  h,
						Mtime: info.ModTime().Unix(),
						Size:  info.Size(),
					}
				}
				s.mu.Unlock()
			}
		}

		s.emit(opMsg, false)
		ok++
	}

	s.mu.Lock()
	for _, pf := range snap {
		if !contains(failed, pf.LocalPath) {
			delete(s.pending, pf.LocalPath)
		}
	}
	s.mu.Unlock()

	if ok > 0 {
		s.emit(fmt.Sprintf("✔ %d file(s) sent.", ok), false)
	}

	s.savePending()
	s.saveHashCache()
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func (s *Session) Start() error {
	if len(s.Container.Folders) == 0 {
		return fmt.Errorf("no folders configured to watch")
	}

	for s.pulling.Load() {
		time.Sleep(20 * time.Millisecond)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}

	watchCount := 0
	for _, folder := range s.Container.Folders {
		n, err := addDirsRecursive(w, folder.LocalPath)
		if err != nil {
			_ = w.Close()
			return fmt.Errorf("watch path %s: %w", folder.LocalPath, err)
		}
		watchCount += n
		s.emit("Watching folder: "+folder.LocalPath, false)
	}

	s.emit(fmt.Sprintf("Session started (mode: %s, watching %d dirs)", s.Container.SyncMode, watchCount), false)
	go s.loop(w)
	return nil
}

func (s *Session) loop(w *fsnotify.Watcher) {
	defer func() { _ = w.Close() }()
	defer close(s.Events)

	debounce := make(map[string]debounceEntry)
	var dmu sync.Mutex

	for {
		select {
		case <-s.stop:
			s.emit("Watcher stopped.", false)
			return

		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			s.emit("Watcher error: "+err.Error(), true)

		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			s.handleEvent(w, ev, debounce, &dmu)
		}
	}
}

func (s *Session) handleEvent(w *fsnotify.Watcher, ev fsnotify.Event, debounce map[string]debounceEntry, dmu *sync.Mutex) {
	path := ev.Name
	op := ev.Op

	if s.pulling.Load() {
		if op&fsnotify.Create != 0 {
			if info, err := os.Stat(path); err == nil && info.IsDir() {
				_, _ = addDirsRecursive(w, path)
			}
		}
		return
	}

	if op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			n, err := addDirsRecursive(w, path)
			if err != nil {
				s.emit("failed to watch new dir "+path+": "+err.Error(), true)
			} else {
				s.emit(fmt.Sprintf("Watching new folder: %s (%d dirs)", path, n), false)
			}
			return
		}
	}

	if s.isIgnored(path) {
		return
	}

	dmu.Lock()
	entry := debounce[path]
	if entry.timer != nil {
		entry.timer.Stop()
	}
	accumulated := entry.op | op
	entry = debounceEntry{
		op: accumulated,
		timer: time.AfterFunc(150*time.Millisecond, func() {
			dmu.Lock()
			finalOp := debounce[path].op
			delete(debounce, path)
			dmu.Unlock()
			s.processEvent(path, finalOp)
		}),
	}
	debounce[path] = entry
	dmu.Unlock()
}

func (s *Session) processEvent(path string, op fsnotify.Op) {
	if s.pulling.Load() {
		return
	}

	if op&(fsnotify.Create|fsnotify.Write) != 0 {
		s.queue(path, KindUpsert)
	} else if op&fsnotify.Remove != 0 && s.Container.DeleteSync {
		s.queue(path, KindDelete)
	}

	if s.Container.SyncMode == config.SyncAuto {
		s.Flush()
	}
}

func (s *Session) queue(localPath string, kind EventKind) {
	if kind == KindUpsert {
		if current, err := hashFile(localPath); err == nil {
			s.mu.Lock()
			known := s.knownHash[localPath]
			s.mu.Unlock()
			if hashesEqual(current, known) {
				s.mu.Lock()
				_, wasPending := s.pending[localPath]
				delete(s.pending, localPath)
				count := len(s.pending)
				s.mu.Unlock()
				if wasPending {
					s.emit(fmt.Sprintf("~ %s reverted, removed from pending (%d total)", filepath.Base(localPath), count), false)
					s.savePending()
				}
				return
			}
		}
	}

	s.mu.Lock()
	s.pending[localPath] = PendingFile{LocalPath: localPath, Kind: kind}
	count := len(s.pending)
	s.mu.Unlock()

	s.emit(fmt.Sprintf("~ %s pending (%d total)", filepath.Base(localPath), count), false)
	s.savePending()
}

func (s *Session) Stop() {
	s.savePending()
	s.saveHashCache()
	s.once.Do(func() { close(s.stop) })
}

func (s *Session) emit(msg string, isErr bool) {
	select {
	case s.Events <- Event{Time: time.Now(), Message: msg, Err: isErr}:
	default:
	}
}

func addDirsRecursive(w *fsnotify.Watcher, root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if err := w.Add(path); err != nil {
			return err
		}
		count++
		return nil
	})
	return count, err
}

type PullResult struct {
	Downloaded []string
	LocalOnly  []string
	Errors     []string
}

type downloadJob struct {
	rf         sftpclient.RemoteFile
	folderBase string
}

func (s *Session) Pull(emit func(string, bool)) (*PullResult, error) {
	s.pulling.Store(true)
	defer s.pulling.Store(false)

	client, err := sftpclient.Connect(s.Container.SFTP)
	if err != nil {
		return nil, fmt.Errorf("SFTP connection error: %w", err)
	}
	defer client.Close()

	result := &PullResult{}

	type folderWork struct {
		jobs      []downloadJob
		remoteSet map[string]struct{}
		localBase string
	}
	var allFolders []folderWork

	for _, folder := range s.Container.Folders {
		remotePath := filepath.ToSlash(filepath.Join(s.Container.SFTP.RemotePath, folder.Name))
		remoteFiles, err := client.ListRemote(remotePath, folder.LocalPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("list remote %s: %v", remotePath, err))
			continue
		}

		fw := folderWork{
			remoteSet: make(map[string]struct{}, len(remoteFiles)),
			localBase: folder.LocalPath,
		}

		for _, rf := range remoteFiles {
			fw.remoteSet[rf.LocalPath] = struct{}{}
			if s.isIgnored(rf.LocalPath) {
				continue
			}
			if !sftpclient.NeedsUpdate(rf.LocalPath, rf) {
				continue
			}
			fw.jobs = append(fw.jobs, downloadJob{rf: rf, folderBase: folder.LocalPath})
		}
		allFolders = append(allFolders, fw)
	}

	totalJobs := 0
	for _, fw := range allFolders {
		totalJobs += len(fw.jobs)
	}

	if totalJobs == 0 {
		for _, fw := range allFolders {
			_ = filepath.WalkDir(fw.localBase, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || s.isIgnored(path) {
					return nil
				}
				if _, exists := fw.remoteSet[path]; !exists {
					result.LocalOnly = append(result.LocalOnly, path)
				}
				return nil
			})
		}
		emit("Already up to date - no files to download.", false)
		return result, nil
	}

	numWorkers := runtime.NumCPU()
	if numWorkers > 8 {
		numWorkers = 8
	}

	type result_t struct {
		job downloadJob
		h   []byte
		err error
	}

	jobsCh := make(chan downloadJob, totalJobs)
	resultsCh := make(chan result_t, totalJobs)

	for i := 0; i < numWorkers; i++ {
		go func() {
			wClient, err := sftpclient.Connect(s.Container.SFTP)
			if err != nil {
				for job := range jobsCh {
					resultsCh <- result_t{job: job, err: err}
				}
				return
			}
			defer wClient.Close()

			for job := range jobsCh {
				dlErr := wClient.Download(job.rf.RemotePath, job.rf.LocalPath)
				if dlErr != nil {
					resultsCh <- result_t{job: job, err: dlErr}
					continue
				}
				h, _ := hashFile(job.rf.LocalPath)
				resultsCh <- result_t{job: job, h: h}
			}
		}()
	}

	// send all jobs
	for _, fw := range allFolders {
		for _, job := range fw.jobs {
			jobsCh <- job
		}
	}
	close(jobsCh)

	for i := 0; i < totalJobs; i++ {
		r := <-resultsCh
		if r.err != nil {
			result.Errors = append(result.Errors, filepath.Base(r.job.rf.LocalPath)+": "+r.err.Error())
			emit("download error "+filepath.Base(r.job.rf.LocalPath)+": "+r.err.Error(), true)
			continue
		}

		// after the write is recognised as "no change" and not queued.
		if r.h != nil {
			info, statErr := os.Stat(r.job.rf.LocalPath)
			s.mu.Lock()
			s.knownHash[r.job.rf.LocalPath] = r.h
			if statErr == nil {
				s.hashCache[r.job.rf.LocalPath] = hashCacheEntry{
					Hash:  r.h,
					Mtime: info.ModTime().Unix(),
					Size:  info.Size(),
				}
			}
			s.mu.Unlock()
		}

		result.Downloaded = append(result.Downloaded, r.job.rf.LocalPath)
		emit("↓ "+filepath.Base(r.job.rf.LocalPath), false)
	}

	for _, fw := range allFolders {
		_ = filepath.WalkDir(fw.localBase, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || s.isIgnored(path) {
				return nil
			}
			if _, exists := fw.remoteSet[path]; !exists {
				result.LocalOnly = append(result.LocalOnly, path)
			}
			return nil
		})
	}

	if len(result.Downloaded) > 0 {
		emit(fmt.Sprintf("✔ %d file(s) pulled.", len(result.Downloaded)), false)
	}

	s.saveHashCache()

	return result, nil
}
