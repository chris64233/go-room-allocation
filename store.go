package goroomallocation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store 是分配依据的持久化接口。所有方法必须保证并发安全，
// 且 Update 在单个临界区内原子地执行读取-修改-写回。
type Store interface {
	// Load 返回当前持久化状态的深拷贝。
	Load() (snapshot, error)
	// Update 在单个事务中读取最新快照、交给 fn 修改并写回。
	// fn 返回错误时不写回；写回内容是 fn 修改后的 snap。
	Update(fn func(snap *snapshot) error) error
}

// MemoryStore 是仅保存在内存中的 Store 实现，测试或单进程场景使用。
type MemoryStore struct {
	mu   sync.Mutex
	snap snapshot
}

// NewMemoryStore 创建空的内存存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{snap: newSnapshot()}
}

func (m *MemoryStore) Load() (snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap.clone(), nil
}

func (m *MemoryStore) Update(fn func(snap *snapshot) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	work := m.snap.clone()
	if err := fn(&work); err != nil {
		return err
	}
	m.snap = work
	return nil
}

// FileStore 把分配依据以 JSON 文件持久化到磁盘，写入采用临时文件 + 原子重命名，
// 保证持久化文件始终完整。
type FileStore struct {
	mu   sync.Mutex
	path string
}

// NewFileStore 使用 path 作为持久化文件。文件不存在时初始化为空状态；
// 已存在则在首次加载时读入。
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s, s.persistLocked(newSnapshot())
	} else if err != nil {
		return nil, err
	}
	if _, err := s.loadLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileStore) loadLocked() (snapshot, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return snapshot{}, err
	}
	snap := newSnapshot()
	if len(data) > 0 {
		if err := json.Unmarshal(data, &snap); err != nil {
			return snapshot{}, err
		}
	}
	if snap.Rooms == nil {
		snap.Rooms = map[string]Room{}
	}
	if snap.Reservations == nil {
		snap.Reservations = map[string]*Reservation{}
	}
	return snap, nil
}

func (s *FileStore) persistLocked(snap snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".room-allocation-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func (s *FileStore) Load() (snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *FileStore) Update(fn func(snap *snapshot) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := s.loadLocked()
	if err != nil {
		return err
	}
	if err := fn(&snap); err != nil {
		return err
	}
	return s.persistLocked(snap)
}
