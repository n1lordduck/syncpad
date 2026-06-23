package watcher

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	LocalPath string
	Kind      EventKind
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

type Session struct {
	Container    *config.Container
	GlobalIgnore []string
	Events       chan Event

	mu      sync.Mutex
	pending map[string]PendingFile

	stop chan struct{}
	once sync.Once
}

func NewSession(c *config.Container, globalIgnore []string) *Session {
	return &Session{
		Container:    c,
		GlobalIgnore: globalIgnore,
		Events:       make(chan Event, 128),
		pending:      make(map[string]PendingFile),
		stop:         make(chan struct{}),
	}
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

		s.emit(opMsg, false)
		ok++
	}

	// single lock acquisition to clear all succeeded files
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[localPath] = PendingFile{LocalPath: localPath, Kind: kind}
	s.emit(fmt.Sprintf("~ %s pending (%d total)", filepath.Base(localPath), len(s.pending)), false)
}

func (s *Session) Stop() {
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

func (s *Session) Pull(emit func(string, bool)) (*PullResult, error) {
	client, err := sftpclient.Connect(s.Container.SFTP)
	if err != nil {
		return nil, fmt.Errorf("SFTP connection error: %w", err)
	}
	defer client.Close()

	result := &PullResult{}

	for _, folder := range s.Container.Folders {
		remotePath := filepath.ToSlash(filepath.Join(s.Container.SFTP.RemotePath, folder.Name))
		remoteFiles, err := client.ListRemote(remotePath, folder.LocalPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("list remote %s: %v", remotePath, err))
			continue
		}

		remoteSet := make(map[string]struct{}, len(remoteFiles))
		for _, rf := range remoteFiles {
			remoteSet[rf.LocalPath] = struct{}{}

			if s.isIgnored(rf.LocalPath) {
				continue
			}

			if !sftpclient.NeedsUpdate(rf.LocalPath, rf) {
				continue
			}

			if err := client.Download(rf.RemotePath, rf.LocalPath); err != nil {
				result.Errors = append(result.Errors, filepath.Base(rf.LocalPath)+": "+err.Error())
				emit("download error "+filepath.Base(rf.LocalPath)+": "+err.Error(), true)
				continue
			}

			result.Downloaded = append(result.Downloaded, rf.LocalPath)
			emit("↓ "+filepath.Base(rf.LocalPath), false)
		}

		_ = filepath.WalkDir(folder.LocalPath, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || s.isIgnored(path) {
				return nil
			}
			if _, exists := remoteSet[path]; !exists {
				result.LocalOnly = append(result.LocalOnly, path)
			}
			return nil
		})
	}

	if len(result.Downloaded) > 0 {
		emit(fmt.Sprintf("✔ %d file(s) pulled.", len(result.Downloaded)), false)
	}

	return result, nil
}
