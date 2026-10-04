package goroomallocation

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Clock 提供墙钟时间，便于测试中控制超时。
type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Option 用于配置 Service。
type Option func(*config)

type config struct {
	clock         Clock
	sweepInterval time.Duration
}

// WithClock 注入自定义时钟（主要用于测试）。
func WithClock(c Clock) Option {
	return func(cfg *config) { cfg.clock = c }
}

// WithSweepInterval 开启后台过期扫描；默认不开启，仅在写入路径上惰性推进。
// 传入非正值同样表示关闭后台扫描。
func WithSweepInterval(d time.Duration) Option {
	return func(cfg *config) { cfg.sweepInterval = d }
}

// Service 是团体预留的应用服务，所有方法均可被多 goroutine 并发调用。
// 并发正确性依赖 Store.Update 提供的原子事务：同一时刻只有一个团体
// 能完成“查可用房 → 锁定房间”，因此不会重复分配。
type Service struct {
	store         Store
	clock         Clock
	ttl           time.Duration
	sweepInterval time.Duration

	janCancel context.CancelFunc
	janDone   chan struct{}
	closeOnce sync.Once
}

// NewService 创建预留服务。ttl 是预留等待确认的最长时限。
func NewService(store Store, ttl time.Duration, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: store is nil", ErrInvalidArgument)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("%w: ttl must be positive", ErrInvalidArgument)
	}
	cfg := config{clock: systemClock{}}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.clock == nil {
		cfg.clock = systemClock{}
	}
	svc := &Service{
		store:         store,
		clock:         cfg.clock,
		ttl:           ttl,
		sweepInterval: cfg.sweepInterval,
	}
	if cfg.sweepInterval > 0 {
		svc.startJanitor(cfg.sweepInterval)
	}
	return svc, nil
}

// NewMemoryService 使用内存存储创建服务。
func NewMemoryService(ttl time.Duration, opts ...Option) (*Service, error) {
	return NewService(NewMemoryStore(), ttl, opts...)
}

// NewFileService 使用 path 指向的 JSON 文件持久化分配依据。
func NewFileService(path string, ttl time.Duration, opts ...Option) (*Service, error) {
	store, err := NewFileStore(path)
	if err != nil {
		return nil, err
	}
	return NewService(store, ttl, opts...)
}

// Close 停止后台过期扫描。可重复调用。
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		if s.janCancel != nil {
			s.janCancel()
			<-s.janDone
		}
	})
	return nil
}

func (s *Service) startJanitor(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.janCancel = cancel
	s.janDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = s.expireAt(s.clock.Now())
			}
		}
	}()
}

// UpsertRooms 新增或更新房间配置。已被有效预留占用的房间不允许变更房型。
func (s *Service) UpsertRooms(rooms ...Room) error {
	for _, room := range rooms {
		if err := room.validate(); err != nil {
			return err
		}
	}
	return s.store.Update(func(snap *snapshot) error {
		for _, room := range rooms {
			if old, ok := snap.Rooms[room.Number]; ok && old.RoomType != room.RoomType {
				if hasActiveAllocation(snap, room.Number) {
					return fmt.Errorf("%w: room %q type cannot change while it is allocated", ErrConflict, room.Number)
				}
			}
			snap.Rooms[room.Number] = room
		}
		return nil
	})
}

// Rooms 返回按楼层、房号排序的全部房间。
func (s *Service) Rooms() ([]Room, error) {
	snap, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	rooms := make([]Room, 0, len(snap.Rooms))
	for _, room := range snap.Rooms {
		rooms = append(rooms, room)
	}
	sort.Slice(rooms, func(i, j int) bool {
		if rooms[i].Floor != rooms[j].Floor {
			return rooms[i].Floor < rooms[j].Floor
		}
		return rooms[i].Number < rooms[j].Number
	})
	return rooms, nil
}

// Reserve 为团体订单在整段入住区间内预留房间。
//
//   - 房间在整个 [CheckIn, CheckOut) 区间内可用才会入选；
//   - 数量不足时整单失败，不产生任何分配（ErrInsufficientRooms）；
//   - 相同外单号重复提交相同需求返回原方案；需求变更返回 ErrConflict；
//   - 预留进入 pending 并锁定房间，TTL 内等待确认。
func (s *Service) Reserve(req Request) (*Reservation, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	var out *Reservation
	err := s.store.Update(func(snap *snapshot) error {
		now := s.clock.Now()
		sweepExpired(snap, now)

		if existing := findByExternal(snap, req.ExternalOrderID); existing != nil {
			if existing.RoomType != req.RoomType ||
				!existing.CheckIn.Equal(req.CheckIn) ||
				!existing.CheckOut.Equal(req.CheckOut) ||
				existing.Count != req.Count {
				return fmt.Errorf("%w: external order %q was already submitted with a different demand", ErrConflict, req.ExternalOrderID)
			}
			out = cloneReservation(existing)
			return nil
		}

		candidates := make([]Room, 0)
		for _, room := range snap.Rooms {
			if room.RoomType != req.RoomType {
				continue
			}
			if snap.isBusy(room.Number, req.CheckIn, req.CheckOut) {
				continue
			}
			candidates = append(candidates, room)
		}
		chosen := chooseRooms(candidates, req.Count)
		if len(chosen) < req.Count {
			return fmt.Errorf("%w: need %d %q room(s) across %s..%s", ErrInsufficientRooms, req.Count, req.RoomType, req.CheckIn, req.CheckOut)
		}

		numbers := make([]string, len(chosen))
		for i, room := range chosen {
			numbers[i] = room.Number
		}
		seq := snap.NextSeq
		snap.NextSeq++
		reservation := &Reservation{
			ID:          fmt.Sprintf("R%06d", seq+1),
			ExternalID:  req.ExternalOrderID,
			CheckIn:     req.CheckIn,
			CheckOut:    req.CheckOut,
			RoomType:    req.RoomType,
			Count:       req.Count,
			RoomNumbers: numbers,
			Status:      StatusPending,
			ExpiresAt:   now.Add(s.ttl),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		snap.Reservations[reservation.ID] = reservation
		out = cloneReservation(reservation)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Confirm 确认外单号对应的预留。仅 pending 预留可以确认；
// 对已取消 / 已过期预留的迟到确认返回 ErrTerminal 且不产生任何副作用。
// 重复确认同一已确认订单按幂等返回原方案。
func (s *Service) Confirm(externalOrderID string) (*Reservation, error) {
	return s.transition(externalOrderID, StatusConfirmed)
}

// Cancel 主动取消外单号对应的 pending 预留并立即释放房间。
// 对已确认或已进入其它终态的预留返回 ErrTerminal。
func (s *Service) Cancel(externalOrderID string) (*Reservation, error) {
	return s.transition(externalOrderID, StatusCancelled)
}

func (s *Service) transition(externalOrderID string, target Status) (*Reservation, error) {
	if externalOrderID == "" {
		return nil, fmt.Errorf("%w: external order id is empty", ErrInvalidArgument)
	}
	var out *Reservation
	err := s.store.Update(func(snap *snapshot) error {
		now := s.clock.Now()
		sweepExpired(snap, now)
		reservation := findByExternal(snap, externalOrderID)
		if reservation == nil {
			return fmt.Errorf("%w: reservation for external order %q", ErrNotFound, externalOrderID)
		}
		// 幂等：重复进入同一终态直接返回原方案。
		if reservation.Status == target {
			out = cloneReservation(reservation)
			return nil
		}
		// 只有 pending 可以流转；确认与取消同时发生时只有一个赢得终态，
		// 另一个在这里被拒绝。迟到确认（预留已过期）也在此被拒绝，
		// 释放出的房间已可被其它订单使用，不受影响。
		if reservation.Status != StatusPending {
			return fmt.Errorf("%w: reservation %q is %s", ErrTerminal, externalOrderID, reservation.Status)
		}
		reservation.Status = target
		reservation.UpdatedAt = now
		out = cloneReservation(reservation)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GetReservation 按外单号查询预留，查询时会先惰性推进过期。
func (s *Service) GetReservation(externalOrderID string) (*Reservation, error) {
	var out *Reservation
	err := s.store.Update(func(snap *snapshot) error {
		sweepExpired(snap, s.clock.Now())
		reservation := findByExternal(snap, externalOrderID)
		if reservation == nil {
			return fmt.Errorf("%w: reservation for external order %q", ErrNotFound, externalOrderID)
		}
		out = cloneReservation(reservation)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListReservations 返回按内部 ID 排序的全部预留记录（含终态，便于审计）。
func (s *Service) ListReservations() ([]*Reservation, error) {
	var snap snapshot
	err := s.store.Update(func(latest *snapshot) error {
		sweepExpired(latest, s.clock.Now())
		snap = latest.clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	list := make([]*Reservation, 0, len(snap.Reservations))
	for _, reservation := range snap.Reservations {
		list = append(list, cloneReservation(reservation))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

// Expire 用当前时钟推进所有超时预留，返回被释放的预留数量。
func (s *Service) Expire() (int, error) {
	return s.expireAt(s.clock.Now())
}

// expireAt 以指定时间推进超时，供后台扫描和测试使用。
func (s *Service) expireAt(now time.Time) (int, error) {
	var ids []string
	err := s.store.Update(func(snap *snapshot) error {
		ids = sweepExpired(snap, now)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

// CalendarEntry 是入住日历上的一条占用记录：某团体在某一晚占用了某间房。
type CalendarEntry struct {
	Date            Date   `json:"date"`
	RoomNumber      string `json:"room_number"`
	RoomType        string `json:"room_type"`
	Floor           int    `json:"floor"`
	ReservationID   string `json:"reservation_id"`
	ExternalOrderID string `json:"external_order_id"`
	// Status 为 pending 或 confirmed；已取消 / 已过期的预留不会出现在日历中。
	Status Status `json:"status"`
}

// Calendar 查询 [from, to) 每晚的房间占用，可按房型过滤（空字符串表示全部房型）。
// 返回结果按日期、楼层、房号、预留 ID 排序；没有条目的“房-晚”组合即为可售。
func (s *Service) Calendar(roomType string, from, to Date) ([]CalendarEntry, error) {
	if from.IsZero() || to.IsZero() || !from.Before(to) {
		return nil, fmt.Errorf("%w: calendar range must be a valid half-open interval", ErrInvalidArgument)
	}
	var snap snapshot
	err := s.store.Update(func(latest *snapshot) error {
		sweepExpired(latest, s.clock.Now())
		snap = latest.clone()
		return nil
	})
	if err != nil {
		return nil, err
	}
	var entries []CalendarEntry
	for _, reservation := range snap.activeReservations() {
		if roomType != "" && reservation.RoomType != roomType {
			continue
		}
		if !overlaps(from, to, reservation.CheckIn, reservation.CheckOut) {
			continue
		}
		nightStart := laterDate(from, reservation.CheckIn)
		nightEnd := earlierDate(to, reservation.CheckOut)
		for night := nightStart; night.Before(nightEnd); night = night.Add(1) {
			for _, number := range reservation.RoomNumbers {
				entries = append(entries, CalendarEntry{
					Date:            night,
					RoomNumber:      number,
					RoomType:        reservation.RoomType,
					Floor:           snap.Rooms[number].Floor,
					ReservationID:   reservation.ID,
					ExternalOrderID: reservation.ExternalID,
					Status:          reservation.Status,
				})
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		if a.Floor != b.Floor {
			return a.Floor < b.Floor
		}
		if a.RoomNumber != b.RoomNumber {
			return a.RoomNumber < b.RoomNumber
		}
		return a.ReservationID < b.ReservationID
	})
	return entries, nil
}

// findByExternal 按外部订单号查找预留；正常数据中每个外单号至多一条。
func findByExternal(snap *snapshot, externalOrderID string) *Reservation {
	for _, reservation := range snap.Reservations {
		if reservation.ExternalID == externalOrderID {
			return reservation
		}
	}
	return nil
}

// hasActiveAllocation 报告某房间是否被任意 pending / confirmed 预留占用。
func hasActiveAllocation(snap *snapshot, roomNumber string) bool {
	for _, reservation := range snap.activeReservations() {
		for _, number := range reservation.RoomNumbers {
			if number == roomNumber {
				return true
			}
		}
	}
	return false
}

// sweepExpired 把所有已超时的 pending 预留翻转为 expired 并释放占用，
// 返回受影响的预留 ID（已排序）。过期只发生一次，重复调用是幂等的。
func sweepExpired(snap *snapshot, now time.Time) []string {
	var ids []string
	for id, reservation := range snap.Reservations {
		if reservation.Status == StatusPending && !reservation.ExpiresAt.After(now) {
			reservation.Status = StatusExpired
			reservation.UpdatedAt = now
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func cloneReservation(r *Reservation) *Reservation {
	if r == nil {
		return nil
	}
	cp := *r
	cp.RoomNumbers = append([]string(nil), r.RoomNumbers...)
	return &cp
}

func laterDate(a, b Date) Date {
	if a.Before(b) {
		return b
	}
	return a
}

func earlierDate(a, b Date) Date {
	if a.Before(b) {
		return a
	}
	return b
}
