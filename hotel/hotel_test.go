package hotel

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func d(day int) Date { return NewDate(2026, time.October, day) }

// newTestHotel 构造两楼层各 4 间标准房（101-104、201-204）的酒店。
func newTestHotel(t *testing.T, store Store, now func() time.Time) *Hotel {
	t.Helper()
	var opts []Option
	if now != nil {
		opts = append(opts, WithClock(now))
	}
	h, err := New(store, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for floor := 1; floor <= 2; floor++ {
		for i := 1; i <= 4; i++ {
			if err := h.AddRoom(Room{Number: floor*100 + i, Floor: floor, Type: "std"}); err != nil {
				t.Fatalf("AddRoom: %v", err)
			}
		}
	}
	return h
}

func reserveReq(orderID string, in, out Date, count int) ReserveRequest {
	return ReserveRequest{
		ExternalOrderID: orderID,
		RoomType:        "std",
		CheckIn:         in,
		CheckOut:        out,
		Count:           count,
		TTL:             30 * time.Minute,
	}
}

func TestReservePicksFloorConcentratedAdjacentRooms(t *testing.T) {
	h := newTestHotel(t, nil, nil)

	// 先占掉一楼的 102，使一楼不再连续。
	first, err := h.Reserve(reserveReq("o1", d(1), d(3), 3))
	if err != nil {
		t.Fatalf("reserve o1: %v", err)
	}
	if want := []int{101, 102, 103}; !reflect.DeepEqual(first.Rooms, want) {
		t.Fatalf("o1 rooms = %v, want %v", first.Rooms, want)
	}

	// 一楼只剩 104 一间，团体要 2 间，应集中到二楼且房号接近。
	second, err := h.Reserve(reserveReq("o2", d(1), d(3), 2))
	if err != nil {
		t.Fatalf("reserve o2: %v", err)
	}
	if want := []int{201, 202}; !reflect.DeepEqual(second.Rooms, want) {
		t.Fatalf("o2 rooms = %v, want %v", second.Rooms, want)
	}
}

func TestReserveFailsAsWholeWhenInsufficient(t *testing.T) {
	h := newTestHotel(t, nil, nil)
	if _, err := h.Reserve(reserveReq("big", d(1), d(2), 9)); !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("err = %v, want ErrInsufficientRooms", err)
	}
	// 整单失败不得留下任何占用。
	res, err := h.Reserve(reserveReq("ok", d(1), d(2), 8))
	if err != nil {
		t.Fatalf("reserve after failed order: %v", err)
	}
	if len(res.Rooms) != 8 {
		t.Fatalf("rooms = %v, want 8 rooms", res.Rooms)
	}
}

func TestReserveRequiresAvailabilityAcrossWholeStay(t *testing.T) {
	h := newTestHotel(t, nil, nil)
	// 8 间房在 [1,2) 被占满，[2,3) 全空。
	if _, err := h.Reserve(reserveReq("full", d(1), d(2), 8)); err != nil {
		t.Fatalf("reserve full: %v", err)
	}
	// [1,3) 跨区间没有任何一间全程可用之外还有余量：整单失败。
	if _, err := h.Reserve(reserveReq("span", d(1), d(3), 1)); !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("err = %v, want ErrInsufficientRooms", err)
	}
	// [2,3) 左闭右开，退房当天即可再次入住。
	if _, err := h.Reserve(reserveReq("next", d(2), d(3), 8)); err != nil {
		t.Fatalf("reserve next: %v", err)
	}
}

func TestIdempotentResubmitAndConflict(t *testing.T) {
	h := newTestHotel(t, nil, nil)
	res, err := h.Reserve(reserveReq("ext-1", d(1), d(3), 2))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	again, err := h.Reserve(reserveReq("ext-1", d(1), d(3), 2))
	if err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if again.ID != res.ID || !reflect.DeepEqual(again.Rooms, res.Rooms) {
		t.Fatalf("resubmit = %+v, want original %+v", again, res)
	}
	// 改变数量或日期都报冲突。
	if _, err := h.Reserve(reserveReq("ext-1", d(1), d(3), 3)); !errors.Is(err, ErrConflict) {
		t.Fatalf("change count err = %v, want ErrConflict", err)
	}
	if _, err := h.Reserve(reserveReq("ext-1", d(1), d(4), 2)); !errors.Is(err, ErrConflict) {
		t.Fatalf("change dates err = %v, want ErrConflict", err)
	}
}

func TestConcurrentReservesNeverDoubleAllocate(t *testing.T) {
	h := newTestHotel(t, nil, nil)
	const orders = 16 // 只有 8 间房，半数订单应失败
	var wg sync.WaitGroup
	results := make([]*Reservation, orders)
	errs := make([]error, orders)
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := h.Reserve(reserveReq(fmt.Sprintf("c-%d", i), d(1), d(2), 1))
			results[i], errs[i] = res, err
		}(i)
	}
	wg.Wait()

	used := make(map[int]string)
	succeeded := 0
	for i := range results {
		if errs[i] != nil {
			if !errors.Is(errs[i], ErrInsufficientRooms) {
				t.Fatalf("unexpected err: %v", errs[i])
			}
			continue
		}
		succeeded++
		for _, room := range results[i].Rooms {
			if owner, dup := used[room]; dup {
				t.Fatalf("room %d allocated to both %s and %s", room, owner, results[i].ID)
			}
			used[room] = results[i].ID
		}
	}
	if succeeded != 8 {
		t.Fatalf("succeeded = %d, want 8", succeeded)
	}
}

func TestSingleTerminalState(t *testing.T) {
	h := newTestHotel(t, nil, nil)
	res, err := h.Reserve(reserveReq("t1", d(1), d(2), 1))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := h.Confirm(res.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	// 确认后再取消/再确认都不得改变终态。
	if err := h.Cancel(res.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("cancel after confirm = %v, want ErrNotPending", err)
	}
	if err := h.Confirm(res.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("re-confirm = %v, want ErrNotPending", err)
	}

	res2, err := h.Reserve(reserveReq("t2", d(5), d(6), 1))
	if err != nil {
		t.Fatalf("reserve t2: %v", err)
	}
	if err := h.Cancel(res2.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := h.Confirm(res2.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("confirm after cancel = %v, want ErrNotPending", err)
	}
}

func TestLateConfirmDoesNotAffectLaterOrders(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	h := newTestHotel(t, nil, clock)

	old, err := h.Reserve(reserveReq("old", d(2), d(4), 2))
	if err != nil {
		t.Fatalf("reserve old: %v", err)
	}
	// 推进时间超过 TTL，旧预留过期，房间被释放。
	now = now.Add(time.Hour)
	if n := h.ExpirePending(); n != 1 {
		t.Fatalf("expired = %d, want 1", n)
	}
	// 后来的订单拿到同一批房间并确认。
	later, err := h.Reserve(reserveReq("later", d(2), d(4), 2))
	if err != nil {
		t.Fatalf("reserve later: %v", err)
	}
	if !reflect.DeepEqual(later.Rooms, old.Rooms) {
		t.Fatalf("later rooms = %v, want reused %v", later.Rooms, old.Rooms)
	}
	if err := h.Confirm(later.ID); err != nil {
		t.Fatalf("confirm later: %v", err)
	}
	// 旧预留的迟到确认失败，且不影响后来的订单。
	if err := h.Confirm(old.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("late confirm = %v, want ErrNotPending", err)
	}
	got, err := h.Get(later.ID)
	if err != nil {
		t.Fatalf("get later: %v", err)
	}
	if got.State != StateConfirmed {
		t.Fatalf("later state = %s, want confirmed", got.State)
	}
}

func TestConfirmAfterDeadlineExpiresReservation(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	h := newTestHotel(t, nil, clock)

	res, err := h.Reserve(reserveReq("lazy", d(2), d(3), 1))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	now = now.Add(time.Hour) // 超过 30 分钟 TTL
	if err := h.Confirm(res.ID); !errors.Is(err, ErrExpired) {
		t.Fatalf("confirm = %v, want ErrExpired", err)
	}
	got, err := h.Get(res.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != StateExpired {
		t.Fatalf("state = %s, want expired", got.State)
	}
	// 房间已被释放，可以再次预订。
	if _, err := h.Reserve(reserveReq("reuse", d(2), d(3), 8)); err != nil {
		t.Fatalf("reuse after lazy expiry: %v", err)
	}
}

func TestCalendarReflectsHalfOpenInterval(t *testing.T) {
	h := newTestHotel(t, nil, nil)
	res, err := h.Reserve(reserveReq("cal", d(1), d(3), 2))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	days, err := h.Calendar(d(1), d(4))
	if err != nil {
		t.Fatalf("calendar: %v", err)
	}
	if len(days) != 3 {
		t.Fatalf("days = %d, want 3", len(days))
	}
	for _, room := range res.Rooms {
		if _, ok := days[0].Occupied[room]; !ok {
			t.Fatalf("room %d should be occupied on %s", room, days[0].Date)
		}
		if _, ok := days[1].Occupied[room]; !ok {
			t.Fatalf("room %d should be occupied on %s", room, days[1].Date)
		}
		if _, ok := days[2].Occupied[room]; ok {
			t.Fatalf("room %d should be free on checkout day %s", room, days[2].Date)
		}
	}
	if days[0].FreeByType["std"] != 6 {
		t.Fatalf("free std on day1 = %d, want 6", days[0].FreeByType["std"])
	}
	if days[2].FreeByType["std"] != 8 {
		t.Fatalf("free std on checkout day = %d, want 8", days[2].FreeByType["std"])
	}
}

func TestPersistenceReloadRestoresAllocations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	h := newTestHotel(t, NewFileStore(path), nil)

	res, err := h.Reserve(reserveReq("persist", d(1), d(3), 2))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := h.Confirm(res.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// 用同一文件重建，占用与幂等记录都应恢复。
	h2, err := New(NewFileStore(path))
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	again, err := h2.Reserve(reserveReq("persist", d(1), d(3), 2))
	if err != nil {
		t.Fatalf("resubmit after reload: %v", err)
	}
	if again.ID != res.ID || !reflect.DeepEqual(again.Rooms, res.Rooms) {
		t.Fatalf("resubmit = %+v, want original %+v", again, res)
	}
	// 已确认订单占用的房间在重启后仍不可重复分配。
	if _, err := h2.Reserve(reserveReq("greedy", d(1), d(3), 8)); !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("err = %v, want ErrInsufficientRooms", err)
	}
	got, err := h2.Get(res.ID)
	if err != nil {
		t.Fatalf("get after reload: %v", err)
	}
	if got.State != StateConfirmed {
		t.Fatalf("state = %s, want confirmed", got.State)
	}
}
