package watcher

import (
	"fmt"
	"io/fs"
	"log"
	"path/filepath"
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

func (s *Session) Flush() {
	s.mu.Lock()
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
		remote := client.RemotePath(s.Container.SFTP, s.Container.LocalPath, pf.LocalPath)
		switch pf.Kind {
		case KindUpsert:
			if err := client.Upload(pf.LocalPath, remote); err != nil {
				s.emit("upload error "+filepath.Base(pf.LocalPath)+": "+err.Error(), true)
				continue
			}
			s.emit("↑ "+filepath.Base(pf.LocalPath), false)
		case KindDelete:
			if err := client.Delete(remote); err != nil {
				s.emit("delete error "+filepath.Base(pf.LocalPath)+": "+err.Error(), true)
				continue
			}
			s.emit("✕ "+filepath.Base(pf.LocalPath), false)
		}
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
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}

	if err := addDirsRecursive(w, s.Container.LocalPath); err != nil {
		w.Close()
		return fmt.Errorf("watch path: %w", err)
	}

	mode := s.Container.SyncMode
	s.emit("Watching "+s.Container.LocalPath+" (modo: "+string(mode)+")", false)

	go func() {
		defer w.Close()

		debounce := make(map[string]*time.Timer)
		var dmu sync.Mutex

		for {
			select {
			case <-s.stop:
				s.emit("Watcher stopped.", false)
				return

			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				path := ev.Name
				op := ev.Op

				dmu.Lock()
				if t, exists := debounce[path]; exists {
					t.Stop()
				}
				debounce[path] = time.AfterFunc(300*time.Millisecond, func() {
					dmu.Lock()
					delete(debounce, path)
					dmu.Unlock()

					switch {
					case op&(fsnotify.Create|fsnotify.Write) != 0:
						s.queue(path, KindUpsert)
					case op&fsnotify.Remove != 0 && s.Container.DeleteSync:
						s.queue(path, KindDelete)
					}

					if mode == config.SyncAuto {
						s.Flush()
					}
				})
				dmu.Unlock()

			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Println("watcher error:", err)
				s.emit("Watcher error: "+err.Error(), true)
			}
		}
	}()

	return nil
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
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}
