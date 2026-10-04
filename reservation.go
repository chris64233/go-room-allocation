package goroomallocation

import (
	"fmt"
	"sort"
	"time"
)

// Status 是团体预留的生命周期状态。
type Status string

const (
	// StatusPending 表示预留已锁定房间、等待确认。
	StatusPending Status = "pending"
	// StatusConfirmed 表示预留已确认，房间占用正式建立。
	StatusConfirmed Status = "confirmed"
	// StatusCancelled 表示预留被主动取消，房间占用已释放。
	StatusCancelled Status = "cancelled"
	// StatusExpired 表示预留超过等待时限未确认，房间占用已释放。
	StatusExpired Status = "expired"
)

// IsTerminal 报告状态是否为终态。
func (s Status) IsTerminal() bool {
	return s == StatusConfirmed || s == StatusCancelled || s == StatusExpired
}

// Request 是团体预留的请求要素，也用于外单号的幂等性比对。
type Request struct {
	// ExternalOrderID 是外部团体订单号，相同订单号重复提交必须返回原方案。
	ExternalOrderID string `json:"external_order_id"`
	// CheckIn 是入住日期（含）。
	CheckIn Date `json:"check_in"`
	// CheckOut 是退房日期（不含），区间按左闭右开处理。
	CheckOut Date `json:"check_out"`
	// RoomType 是所需房型。
	RoomType string `json:"room_type"`
	// Count 是需要的房间数。
	Count int `json:"count"`
}

// Reservation 是一次团体预留的完整记录。
type Reservation struct {
	ID          string   `json:"id"`
	ExternalID  string   `json:"external_order_id"`
	CheckIn     Date     `json:"check_in"`
	CheckOut    Date     `json:"check_out"`
	RoomType    string   `json:"room_type"`
	Count       int      `json:"count"`
	RoomNumbers []string `json:"room_numbers"`
	Status      Status   `json:"status"`
	// ExpiresAt 是等待确认的截止墙钟时间。
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (r Request) validate() error {
	if r.ExternalOrderID == "" {
		return fmt.Errorf("%w: external order id is empty", ErrInvalidArgument)
	}
	if r.CheckIn.IsZero() || r.CheckOut.IsZero() {
		return fmt.Errorf("%w: stay dates are required", ErrInvalidArgument)
	}
	if !r.CheckIn.Before(r.CheckOut) {
		return fmt.Errorf("%w: check-out must be after check-in", ErrInvalidArgument)
	}
	if r.RoomType == "" {
		return fmt.Errorf("%w: room type is empty", ErrInvalidArgument)
	}
	if r.Count <= 0 {
		return fmt.Errorf("%w: room count must be positive", ErrInvalidArgument)
	}
	return nil
}

// snapshot 是存储层原子读写的全部持久化状态。
// 房间占用依据 = 房间配置 + 所有非取消/过期预留的分配（含 pending）。
type snapshot struct {
	Version      int                     `json:"version"`
	NextSeq      int                     `json:"next_seq"`
	Rooms        map[string]Room         `json:"rooms"`
	Reservations map[string]*Reservation `json:"reservations"`
}

func newSnapshot() snapshot {
	return snapshot{
		Version:      1,
		Rooms:        map[string]Room{},
		Reservations: map[string]*Reservation{},
	}
}

func (s snapshot) clone() snapshot {
	cp := snapshot{
		Version:      s.Version,
		NextSeq:      s.NextSeq,
		Rooms:        make(map[string]Room, len(s.Rooms)),
		Reservations: make(map[string]*Reservation, len(s.Reservations)),
	}
	for k, v := range s.Rooms {
		cp.Rooms[k] = v
	}
	for k, v := range s.Reservations {
		rc := *v
		rc.RoomNumbers = append([]string(nil), v.RoomNumbers...)
		cp.Reservations[k] = &rc
	}
	return cp
}

// activeReservations 返回仍持有房间占用的预留（pending 或 confirmed），按 ID 排序。
func (s snapshot) activeReservations() []*Reservation {
	out := make([]*Reservation, 0, len(s.Reservations))
	for _, r := range s.Reservations {
		if r.Status == StatusPending || r.Status == StatusConfirmed {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// isBusy 报告某房间在 [checkIn, checkOut) 内是否已被占用。
// pending 与 confirmed 预留都持有占用；cancelled / expired 已释放。
func (s snapshot) isBusy(roomNumber string, checkIn, checkOut Date) bool {
	for _, r := range s.activeReservations() {
		for _, n := range r.RoomNumbers {
			if n == roomNumber && overlaps(checkIn, checkOut, r.CheckIn, r.CheckOut) {
				return true
			}
		}
	}
	return false
}
