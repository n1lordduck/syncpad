package watcher

import (
	"fmt"
	"io/fs"
	"log"
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

type Session struct {
	Container *config.Container
	Events    chan Event

	mu      sync.Mutex
	pending map[string]PendingFile

	stop chan struct{}
	once sync.Once
}

func NewSession(c *config.Container) *Session {
	return &Session{
		Container: c,
		Events:    make(chan Event, 64),
		pending:   make(map[string]PendingFile),
		stop:      make(chan struct{}),
	}
}

func (s *Session) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

func (s *Session) resolveRemotePath(localFilePath string) (string, bool) {
	for _, folder := range s.Container.Folders {
		if !strings.HasPrefix(localFilePath, folder.LocalPath) {
			continue
		}
		rel, err := filepath.Rel(folder.LocalPath, localFilePath)
		if err != nil {
			continue
		}
		remoteTarget := filepath.Join(s.Container.SFTP.RemotePath, folder.Name, rel)
		return filepath.ToSlash(remoteTarget), true
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

	ok := 0
	for _, pf := range snap {
		remote, found := s.resolveRemotePath(pf.LocalPath)
		if !found {
			s.emit("error: could not resolve remote destination for "+filepath.Base(pf.LocalPath), true)
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
			continue
		}

		s.emit(opMsg, false)
		ok++

		s.mu.Lock()
		delete(s.pending, pf.LocalPath)
		s.mu.Unlock()
	}

	if ok > 0 {
		s.emit(fmt.Sprintf("✔ %d file(s) sent.", ok), false)
	}
}

func (s *Session) Start() error {
	if len(s.Container.Folders) == 0 {
		return fmt.Errorf("no folders configured to watch")
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}

	for _, folder := range s.Container.Folders {
		if err := addDirsRecursive(w, folder.LocalPath); err != nil {
			_ = w.Close()
			return fmt.Errorf("watch path %s: %w", folder.LocalPath, err)
		}
		s.emit("Watching folder: "+folder.LocalPath, false)
	}

	mode := s.Container.SyncMode
	s.emit("Session started (mode: "+string(mode)+")", false)

	go s.loop(w, mode)

	return nil
}

func (s *Session) loop(w *fsnotify.Watcher, mode config.SyncMode) {
	defer w.Close()

	debounce := make(map[string]*time.Timer)
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
			log.Println("watcher error:", err)
			s.emit("Watcher error: "+err.Error(), true)

		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			s.handleEvent(w, ev, mode, debounce, &dmu)
		}
	}
}

func (s *Session) handleEvent(w *fsnotify.Watcher, ev fsnotify.Event, mode config.SyncMode, debounce map[string]*time.Timer, dmu *sync.Mutex) {
	path := ev.Name
	op := ev.Op

	if op&fsnotify.Create != 0 {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if err := addDirsRecursive(w, path); err != nil {
				s.emit("failed to watch new dir "+path+": "+err.Error(), true)
			} else {
				s.emit("Watching new folder: "+path, false)
			}
			return
		}
	}

	dmu.Lock()
	if t, exists := debounce[path]; exists {
		t.Stop()
	}
	debounce[path] = time.AfterFunc(300*time.Millisecond, func() {
		dmu.Lock()
		delete(debounce, path)
		dmu.Unlock()

		s.processEvent(path, op, mode)
	})
	dmu.Unlock()
}

func (s *Session) processEvent(path string, op fsnotify.Op, mode config.SyncMode) {
	if op&(fsnotify.Create|fsnotify.Write) != 0 {
		s.queue(path, KindUpsert)
	}

	if op&fsnotify.Remove != 0 && s.Container.DeleteSync {
		s.queue(path, KindDelete)
	}

	if mode == config.SyncAuto {
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

func addDirsRecursive(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		return w.Add(path)
	})
}
