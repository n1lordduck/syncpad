package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

type AuthType string

const (
	AuthPassword AuthType = "password"
	AuthKey      AuthType = "key"
)

type SFTPConfig struct {
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	User       string   `json:"user"`
	Auth       AuthType `json:"auth"`
	Password   string   `json:"password,omitempty"`
	KeyPath    string   `json:"key_path,omitempty"`
	RemotePath string   `json:"remote_path"`
}

type SyncMode string

const (
	SyncManual SyncMode = "manual"
	SyncAuto   SyncMode = "auto"
)

type FolderItem struct {
	Name       string `json:"name"`
	LocalPath  string `json:"local_path"`
	RemotePath string `json:"remote_path"`
}

func (f FolderItem) GetRemotePath(baseRemotePath string) string {
	return filepath.ToSlash(filepath.Join(baseRemotePath, f.Name))
}

var DefaultIgnorePatterns = []string{
	".syncignore",
	".syncpadignore",
	".ignore",
	".env",
	".env.*",
	"*.log",
	"*.tmp",
	"*.swp",
	"*.DS_Store",
	"Thumbs.db",
}

type Container struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	SFTP           SFTPConfig   `json:"sftp"`
	Folders        []FolderItem `json:"folders"`
	SyncMode       SyncMode     `json:"sync_mode"`
	DeleteSync     bool         `json:"delete_sync"`
	IgnorePatterns []string     `json:"ignore_patterns,omitempty"`
}

type Store struct {
	Containers   []*Container `json:"containers"`
	GlobalIgnore []string     `json:"global_ignore,omitempty"`

	mu   sync.RWMutex
	path string
}

func configDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "syncpad")
	return dir, os.MkdirAll(dir, 0700)
}

func Load() (*Store, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "containers.json")
	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	return s, json.Unmarshal(data, s)
}

func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

func (s *Store) Add(c *Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	s.Containers = append(s.Containers, c)
}

func (s *Store) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.Containers[:0]
	for _, c := range s.Containers {
		if c.ID != id {
			out = append(out, c)
		}
	}
	s.Containers = out
}

func (s *Store) Update(updated *Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, c := range s.Containers {
		if c.ID == updated.ID {
			s.Containers[i] = updated
			return
		}
	}
}

func (s *Store) All() []*Container {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Container, len(s.Containers))
	copy(out, s.Containers)
	return out
}

func (s *Store) GetGlobalIgnore() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.GlobalIgnore))
	copy(out, s.GlobalIgnore)
	return out
}

func (s *Store) SetGlobalIgnore(patterns []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.GlobalIgnore = patterns
}
