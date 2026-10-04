// Package hotel 实现酒店团体订单的整组房间预留。
//
// 核心语义：
//   - 入住区间按左闭右开 [checkIn, checkOut) 处理，退房当天不占用房间；
//   - 团体预留要求同一批房间在整个区间内都可用，数量不足则整单失败；
//   - 分房按“楼层集中、房号接近”给出确定方案；
//   - 预留在 TTL 内等待确认，确认 / 取消 / 过期三者只有一个终态生效；
//   - 相同外部订单号重复提交相同需求返回原方案，需求变化则报冲突。
package hotel

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// State 是预留单的生命周期状态。
type State string

const (
	StatePending   State = "pending"   // 已预留，等待确认
	StateConfirmed State = "confirmed" // 已确认（终态）
	StateCancelled State = "cancelled" // 已主动取消（终态）
	StateExpired   State = "expired"   // 预留超时（终态）
)

// Terminal 报告状态是否为终态。
func (s State) Terminal() bool {
	return s == StateConfirmed || s == StateCancelled || s == StateExpired
}

var (
	// ErrInsufficientRooms 表示整个入住区间内可用房间不足，整单失败。
	ErrInsufficientRooms = errors.New("hotel: insufficient rooms for the whole stay")
	// ErrConflict 表示相同外部订单号提交了不同的需求。
	ErrConflict = errors.New("hotel: external order already exists with a different request")
	// ErrNotFound 表示预留单不存在。
	ErrNotFound = errors.New("hotel: reservation not found")
	// ErrNotPending 表示预留单已处于终态，不能再确认或取消。
	ErrNotPending = errors.New("hotel: reservation is no longer pending")
	// ErrExpired 表示预留已超时，确认失败。
	ErrExpired = errors.New("hotel: reservation hold has expired")
)

// Room 是一间客房的静态配置。
type Room struct {
	Number int    `json:"number"`
	Floor  int    `json:"floor"`
	Type   string `json:"type"`
}

// ReserveRequest 描述一次团体预留需求。
type ReserveRequest struct {
	ExternalOrderID string        // 外部订单号，用于幂等；为空则不参与幂等
	RoomType        string        // 房型
	CheckIn         Date          // 入住日（含）
	CheckOut        Date          // 退房日（不含）
	Count           int           // 需要的房间数
	TTL             time.Duration // 预留等待确认的时限
}

// Reservation 是一纸预留单，记录了分配依据（需求、房号、时限、状态）。
type Reservation struct {
	ID              string    `json:"id"`
	ExternalOrderID string    `json:"externalOrderId,omitempty"`
	RoomType        string    `json:"roomType"`
	CheckIn         Date      `json:"checkIn"`
	CheckOut        Date      `json:"checkOut"`
	Count           int       `json:"count"`
	Rooms           []int     `json:"rooms"`
	State           State     `json:"state"`
	CreatedAt       time.Time `json:"createdAt"`
	Deadline        time.Time `json:"deadline"`
}

// sameDemand 判断两次需求是否一致（用于幂等判定）。
func (r *Reservation) sameDemand(req ReserveRequest) bool {
	return r.RoomType == req.RoomType &&
		r.CheckIn.Equal(req.CheckIn) &&
		r.CheckOut.Equal(req.CheckOut) &&
		r.Count == req.Count
}

// DayStatus 是入住日历中某一天的状态。
type DayStatus struct {
	Date       Date
	Occupied   map[int]string // 房号 -> 占用它的预留单 ID
	FreeByType map[string]int // 房型 -> 当日剩余间数
}

// Hotel 是并发安全的房间库存与预留服务。
type Hotel struct {
	mu     sync.Mutex
	now    func() time.Time
	store  Store
	nextID int

	rooms  map[int]Room // 房号 -> 房间
	sorted []Room       // 按 (楼层, 房号) 排序的全部房间

	byID       map[string]*Reservation // 预留单 ID -> 预留单
	byExternal map[string]*Reservation // 外部订单号 -> 预留单
	occupancy  map[int]map[Date]string // 房号 -> 日期 -> 预留单 ID
}

// Option 定制 Hotel 行为。
type Option func(*Hotel)

// WithClock 注入时钟，便于测试推进时间。
func WithClock(now func() time.Time) Option {
	return func(h *Hotel) { h.now = now }
}

// New 创建一个 Hotel，并从 store 恢复房间配置与历史预留。
// store 传 nil 时使用内存存储（不持久化）。
func New(store Store, opts ...Option) (*Hotel, error) {
	if store == nil {
		store = NewMemStore()
	}
	h := &Hotel{
		now:        time.Now,
		store:      store,
		nextID:     1,
		rooms:      make(map[int]Room),
		byID:       make(map[string]*Reservation),
		byExternal: make(map[string]*Reservation),
		occupancy:  make(map[int]map[Date]string),
	}
	for _, opt := range opts {
		opt(h)
	}
	snap, err := store.Load()
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return h, nil
	}
	for _, r := range snap.Rooms {
		h.rooms[r.Number] = r
	}
	h.resortRooms()
	h.nextID = snap.NextID
	if h.nextID < 1 {
		h.nextID = 1
	}
	for _, res := range snap.Reservations {
		cp := res
		h.byID[cp.ID] = &cp
		if cp.ExternalOrderID != "" {
			h.byExternal[cp.ExternalOrderID] = &cp
		}
		if cp.State == StatePending || cp.State == StateConfirmed {
			h.lockRooms(&cp)
		}
	}
	return h, nil
}

// AddRoom 登记一间客房。房号重复或房型为空时返回错误。
func (h *Hotel) AddRoom(room Room) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if room.Type == "" {
		return errors.New("hotel: room type must not be empty")
	}
	if _, ok := h.rooms[room.Number]; ok {
		return fmt.Errorf("hotel: room %d already exists", room.Number)
	}
	h.rooms[room.Number] = room
	h.resortRooms()
	return h.persistLocked()
}

// Rooms 返回按 (楼层, 房号) 排序的房间配置快照。
func (h *Hotel) Rooms() []Room {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Room(nil), h.sorted...)
}

// Reserve 为团体订单预留整组房间。
//
// 相同外部订单号重复提交相同需求时返回原方案；需求不同则返回 ErrConflict。
// 整个区间内可用房间不足时返回 ErrInsufficientRooms，不产生任何部分占用。
func (h *Hotel) Reserve(req ReserveRequest) (*Reservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if req.Count <= 0 {
		return nil, errors.New("hotel: room count must be positive")
	}
	if !req.CheckIn.Before(req.CheckOut) {
		return nil, errors.New("hotel: check-out must be after check-in")
	}
	if req.TTL <= 0 {
		return nil, errors.New("hotel: TTL must be positive")
	}

	if req.ExternalOrderID != "" {
		if existing, ok := h.byExternal[req.ExternalOrderID]; ok {
			if !existing.sameDemand(req) {
				return nil, ErrConflict
			}
			cp := *existing
			return &cp, nil
		}
	}

	nights := EachNight(req.CheckIn, req.CheckOut)
	var candidates []Room
	for _, room := range h.sorted {
		if room.Type != req.RoomType {
			continue
		}
		if h.freeForNightsLocked(room.Number, nights) {
			candidates = append(candidates, room)
		}
	}
	if len(candidates) < req.Count {
		return nil, ErrInsufficientRooms
	}

	picked := chooseRooms(candidates, req.Count)
	now := h.now()
	res := &Reservation{
		ID:              fmt.Sprintf("R%06d", h.nextID),
		ExternalOrderID: req.ExternalOrderID,
		RoomType:        req.RoomType,
		CheckIn:         req.CheckIn,
		CheckOut:        req.CheckOut,
		Count:           req.Count,
		Rooms:           picked,
		State:           StatePending,
		CreatedAt:       now,
		Deadline:        now.Add(req.TTL),
	}
	h.nextID++
	h.byID[res.ID] = res
	if res.ExternalOrderID != "" {
		h.byExternal[res.ExternalOrderID] = res
	}
	h.lockRooms(res)
	if err := h.persistLocked(); err != nil {
		return nil, err
	}
	cp := *res
	return &cp, nil
}

// Confirm 确认预留单。预留已超时则推进为过期并返回 ErrExpired；
// 已处于终态（含迟到的确认）返回 ErrNotPending，不影响任何后续订单。
func (h *Hotel) Confirm(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	res, ok := h.byID[id]
	if !ok {
		return ErrNotFound
	}
	if res.State != StatePending {
		return ErrNotPending
	}
	if !h.now().Before(res.Deadline) {
		h.expireLocked(res)
		if err := h.persistLocked(); err != nil {
			return err
		}
		return ErrExpired
	}
	res.State = StateConfirmed
	return h.persistLocked()
}

// Cancel 主动取消等待中的预留单并释放房间。终态单据返回 ErrNotPending。
func (h *Hotel) Cancel(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	res, ok := h.byID[id]
	if !ok {
		return ErrNotFound
	}
	if res.State != StatePending {
		return ErrNotPending
	}
	res.State = StateCancelled
	h.releaseRooms(res)
	return h.persistLocked()
}

// ExpirePending 推进过期：把所有已到期的等待中预留单置为过期并释放房间，
// 返回本次过期的单据数量。
func (h *Hotel) ExpirePending() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	expired := 0
	for _, res := range h.byID {
		if res.State == StatePending && !now.Before(res.Deadline) {
			h.expireLocked(res)
			expired++
		}
	}
	if expired > 0 {
		_ = h.persistLocked()
	}
	return expired
}

// Get 按预留单 ID 查询单据快照。
func (h *Hotel) Get(id string) (*Reservation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	res, ok := h.byID[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *res
	return &cp, nil
}

// Calendar 查询 [from, to) 区间内每一天的入住日历。
func (h *Hotel) Calendar(from, to Date) ([]DayStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !from.Before(to) {
		return nil, errors.New("hotel: calendar end must be after start")
	}
	var days []DayStatus
	for d := from; d.Before(to); d = d.Next() {
		status := DayStatus{
			Date:       d,
			Occupied:   make(map[int]string),
			FreeByType: make(map[string]int),
		}
		for _, room := range h.sorted {
			if resID, taken := h.occupancy[room.Number][d]; taken {
				status.Occupied[room.Number] = resID
			} else {
				status.FreeByType[room.Type]++
			}
		}
		days = append(days, status)
	}
	return days, nil
}

// chooseRooms 在候选房间（已按楼层、房号排序）中选出确定的一组：
// 优先跨楼层数最少，其次房号跨度最小，再次起始房号最小。
func chooseRooms(candidates []Room, count int) []int {
	bestStart := -1
	var bestFloors, bestSpan int
	for i := 0; i+count <= len(candidates); i++ {
		window := candidates[i : i+count]
		floors := 1
		for j := 1; j < len(window); j++ {
			if window[j].Floor != window[j-1].Floor {
				floors++
			}
		}
		span := window[len(window)-1].Number - window[0].Number
		if bestStart == -1 ||
			floors < bestFloors ||
			(floors == bestFloors && span < bestSpan) {
			bestStart, bestFloors, bestSpan = i, floors, span
		}
	}
	picked := make([]int, 0, count)
	for _, room := range candidates[bestStart : bestStart+count] {
		picked = append(picked, room.Number)
	}
	return picked
}

func (h *Hotel) freeForNightsLocked(number int, nights []Date) bool {
	booked := h.occupancy[number]
	for _, night := range nights {
		if _, taken := booked[night]; taken {
			return false
		}
	}
	return true
}

// lockRooms 建立房间占用。仅在预留创建与状态恢复时调用，保证只建立一次。
func (h *Hotel) lockRooms(res *Reservation) {
	for _, number := range res.Rooms {
		if h.occupancy[number] == nil {
			h.occupancy[number] = make(map[Date]string)
		}
		for _, night := range EachNight(res.CheckIn, res.CheckOut) {
			h.occupancy[number][night] = res.ID
		}
	}
}

// releaseRooms 释放房间占用。仅在取消与过期时调用，保证只释放一次。
func (h *Hotel) releaseRooms(res *Reservation) {
	for _, number := range res.Rooms {
		booked := h.occupancy[number]
		for _, night := range EachNight(res.CheckIn, res.CheckOut) {
			if booked[night] == res.ID {
				delete(booked, night)
			}
		}
		if len(booked) == 0 {
			delete(h.occupancy, number)
		}
	}
}

func (h *Hotel) expireLocked(res *Reservation) {
	res.State = StateExpired
	h.releaseRooms(res)
}

func (h *Hotel) resortRooms() {
	h.sorted = h.sorted[:0]
	for _, room := range h.rooms {
		h.sorted = append(h.sorted, room)
	}
	sort.Slice(h.sorted, func(i, j int) bool {
		if h.sorted[i].Floor != h.sorted[j].Floor {
			return h.sorted[i].Floor < h.sorted[j].Floor
		}
		return h.sorted[i].Number < h.sorted[j].Number
	})
}

func (h *Hotel) persistLocked() error {
	snap := Snapshot{
		Rooms:  append([]Room(nil), h.sorted...),
		NextID: h.nextID,
	}
	for _, res := range h.byID {
		snap.Reservations = append(snap.Reservations, *res)
	}
	sort.Slice(snap.Reservations, func(i, j int) bool {
		return snap.Reservations[i].ID < snap.Reservations[j].ID
	})
	return h.store.Save(snap)
}
