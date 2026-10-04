package hotel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Snapshot 是分配依据的持久化快照：房间配置、全部预留单与发号器。
type Snapshot struct {
	Rooms        []Room        `json:"rooms"`
	Reservations []Reservation `json:"reservations"`
	NextID       int           `json:"nextId"`
}

// Store 负责快照的加载与保存。Save 在 Hotel 锁内被调用，实现无需自行加锁，
// 但独立的实现（如 MemStore 被多处使用时）仍应保持并发安全。
type Store interface {
	Load() (*Snapshot, error)
	Save(Snapshot) error
}

// MemStore 是内存版 Store，不持久化，主要用于测试。
type MemStore struct {
	mu   sync.Mutex
	snap *Snapshot
}

// NewMemStore 创建内存存储。
func NewMemStore() *MemStore { return &MemStore{} }

// Load 实现 Store。
func (m *MemStore) Load() (*Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.snap == nil {
		return nil, nil
	}
	cp := *m.snap
	return &cp, nil
}

// Save 实现 Store。
func (m *MemStore) Save(s Snapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snap = &s
	return nil
}

// FileStore 把快照以 JSON 形式写入单个文件，写入采用临时文件 + 原子重命名。
type FileStore struct {
	path string
}

// NewFileStore 创建基于指定路径的文件存储。
func NewFileStore(path string) *FileStore { return &FileStore{path: path} }

// Load 实现 Store；文件不存在时返回空快照。
func (f *FileStore) Load() (*Snapshot, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hotel: load snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("hotel: decode snapshot: %w", err)
	}
	return &snap, nil
}

// Save 实现 Store。
func (f *FileStore) Save(s Snapshot) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("hotel: encode snapshot: %w", err)
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("hotel: write snapshot: %w", err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("hotel: commit snapshot: %w", err)
	}
	return nil
}

// DirFileStore 返回放在指定目录下固定文件名的 FileStore。
func DirFileStore(dir string) *FileStore {
	return NewFileStore(filepath.Join(dir, "hotel-snapshot.json"))
}
