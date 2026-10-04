package goroomallocation

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestService(t *testing.T, opts ...Option) (*Service, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	allOpts := append([]Option{WithClock(clock)}, opts...)
	svc, err := NewMemoryService(time.Minute, allOpts...)
	if err != nil {
		t.Fatalf("NewMemoryService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	seedRooms(t, svc)
	return svc, clock
}

func seedRooms(t *testing.T, svc *Service) {
	t.Helper()
	rooms := []Room{
		{Number: "101", Floor: 1, RoomType: "STD"},
		{Number: "102", Floor: 1, RoomType: "STD"},
		{Number: "201", Floor: 2, RoomType: "STD"},
		{Number: "202", Floor: 2, RoomType: "STD"},
		{Number: "203", Floor: 2, RoomType: "STD"},
		{Number: "301", Floor: 3, RoomType: "DLX"},
	}
	if err := svc.UpsertRooms(rooms...); err != nil {
		t.Fatalf("UpsertRooms: %v", err)
	}
}

func groupReq(id string, in, out string, roomType string, count int) Request {
	return Request{
		ExternalOrderID: id,
		CheckIn:         MustParseDate(in),
		CheckOut:        MustParseDate(out),
		RoomType:        roomType,
		Count:           count,
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestReserveSelectsConcentratedRooms(t *testing.T) {
	svc, _ := newTestService(t)
	r, err := svc.Reserve(groupReq("G1", "2026-10-01", "2026-10-03", "STD", 3))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if r.Status != StatusPending {
		t.Fatalf("status = %s, want pending", r.Status)
	}
	if want := []string{"201", "202", "203"}; !equalStrings(r.RoomNumbers, want) {
		t.Fatalf("rooms = %v, want %v", r.RoomNumbers, want)
	}
}

func TestReserveWholeOrderFailsWhenShort(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.Reserve(groupReq("G1", "2026-10-01", "2026-10-05", "DLX", 2))
	if !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("err = %v, want ErrInsufficientRooms", err)
	}
	list, err := svc.ListReservations()
	if err != nil {
		t.Fatalf("ListReservations: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("failed reservation must not be persisted, got %d records", len(list))
	}
	r, err := svc.Reserve(groupReq("G2", "2026-10-01", "2026-10-05", "DLX", 1))
	if err != nil {
		t.Fatalf("DLX room should still be bookable: %v", err)
	}
	if !equalStrings(r.RoomNumbers, []string{"301"}) {
		t.Fatalf("rooms = %v, want [301]", r.RoomNumbers)
	}
}

func TestRoomsMustBeAvailableAcrossWholeStay(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Reserve(groupReq("G1", "2026-10-01", "2026-10-05", "STD", 3)); err != nil {
		t.Fatalf("Reserve G1: %v", err)
	}
	if _, err := svc.Reserve(groupReq("G2", "2026-10-04", "2026-10-06", "STD", 3)); !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("overlapping reserve err = %v, want ErrInsufficientRooms", err)
	}
	r, err := svc.Reserve(groupReq("G3", "2026-10-05", "2026-10-06", "STD", 3))
	if err != nil {
		t.Fatalf("back-to-back reserve: %v", err)
	}
	if !equalStrings(r.RoomNumbers, []string{"201", "202", "203"}) {
		t.Fatalf("rooms = %v, want 201/202/203", r.RoomNumbers)
	}
	r, err = svc.Reserve(groupReq("G4", "2026-10-10", "2026-10-12", "STD", 4))
	if err != nil {
		t.Fatalf("non-overlapping reserve: %v", err)
	}
	if !equalStrings(r.RoomNumbers, []string{"101", "102", "201", "202"}) {
		t.Fatalf("rooms = %v, want 101/102/201/202", r.RoomNumbers)
	}
}

func TestReserveIdempotencyAndConflict(t *testing.T) {
	svc, _ := newTestService(t)
	req := groupReq("G1", "2026-10-01", "2026-10-04", "STD", 2)
	first, err := svc.Reserve(req)
	if err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	second, err := svc.Reserve(req)
	if err != nil {
		t.Fatalf("idempotent Reserve: %v", err)
	}
	if first.ID != second.ID || !equalStrings(first.RoomNumbers, second.RoomNumbers) {
		t.Fatalf("duplicate submit should return same plan: %+v vs %+v", first, second)
	}

	cases := []Request{
		groupReq("G1", "2026-10-02", "2026-10-04", "STD", 2),
		groupReq("G1", "2026-10-01", "2026-10-05", "STD", 2),
		groupReq("G1", "2026-10-01", "2026-10-04", "STD", 3),
		groupReq("G1", "2026-10-01", "2026-10-04", "DLX", 2),
	}
	for i, changed := range cases {
		if _, err := svc.Reserve(changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("case %d: err = %v, want ErrConflict", i, err)
		}
	}

	confirmed, err := svc.Confirm("G1")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	again, err := svc.Reserve(req)
	if err != nil {
		t.Fatalf("Reserve after confirm: %v", err)
	}
	if again.ID != confirmed.ID || again.Status != StatusConfirmed {
		t.Fatalf("got %+v, want confirmed original %s", again, confirmed.ID)
	}
}

func TestConcurrentReserveNoDoubleAllocation(t *testing.T) {
	svc, _ := newTestService(t)
	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	results := make([]*Reservation, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := svc.Reserve(groupReq(fmt.Sprintf("CG%02d", i), "2026-10-01", "2026-10-03", "STD", 1))
			errs[i] = err
			if err == nil {
				results[i] = r
			}
		}(i)
	}
	close(start)
	wg.Wait()

	success, failed := 0, 0
	seen := map[string]string{}
	for i, err := range errs {
		switch {
		case err == nil:
			success++
			for _, number := range results[i].RoomNumbers {
				if owner, ok := seen[number]; ok {
					t.Fatalf("room %s allocated twice: %s and %s", number, owner, results[i].ExternalID)
				}
				seen[number] = results[i].ExternalID
			}
		case errors.Is(err, ErrInsufficientRooms):
			failed++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if success != 5 || failed != 45 {
		t.Fatalf("success = %d, failed = %d, want 5/45", success, failed)
	}
	if len(seen) != 5 {
		t.Fatalf("allocated room count = %d, want 5", len(seen))
	}
}

func TestConcurrentReserveSameExternalIsIdempotent(t *testing.T) {
	svc, _ := newTestService(t)
	const n = 30
	var wg sync.WaitGroup
	ids := make(chan string, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r, err := svc.Reserve(groupReq("DUP", "2026-10-01", "2026-10-03", "STD", 2))
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			ids <- r.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	first, count := "", 0
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("idempotent duplicate produced different ids: %q vs %q", first, id)
		}
		count++
	}
	if count != n {
		t.Fatalf("successful calls = %d, want %d", count, n)
	}
}

func TestConfirmCancelRaceSingleTerminal(t *testing.T) {
	const rounds = 100
	for i := 0; i < rounds; i++ {
		svc, _ := newTestService(t)
		ext := fmt.Sprintf("RACE-%03d", i)
		if _, err := svc.Reserve(groupReq(ext, "2026-11-01", "2026-11-03", "STD", 1)); err != nil {
			t.Fatalf("Reserve %s: %v", ext, err)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		confirmed, cancelled, terminal := 0, 0, 0
		race := func(act func() (*Reservation, error), mark *int) {
			defer wg.Done()
			r, err := act()
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				*mark++
			case errors.Is(err, ErrTerminal):
				terminal++
			default:
				t.Errorf("unexpected error for %s: %v", ext, err)
			}
			_ = r
		}
		wg.Add(2)
		go race(func() (*Reservation, error) { return svc.Confirm(ext) }, &confirmed)
		go race(func() (*Reservation, error) { return svc.Cancel(ext) }, &cancelled)
		wg.Wait()
		if confirmed+cancelled != 1 || terminal != 1 {
			t.Fatalf("round %d: confirmed=%d cancelled=%d terminal=%d, want exactly one winner and one terminal", i, confirmed, cancelled, terminal)
		}
		final, err := svc.GetReservation(ext)
		if err != nil {
			t.Fatalf("GetReservation: %v", err)
		}
		if !final.Status.IsTerminal() {
			t.Fatalf("final status = %s", final.Status)
		}
	}
}

func TestExpiryReleasesRoomsAndLateConfirmFails(t *testing.T) {
	svc, clock := newTestService(t)
	old, err := svc.Reserve(groupReq("OLD", "2026-10-01", "2026-10-05", "STD", 3))
	if err != nil {
		t.Fatalf("Reserve OLD: %v", err)
	}

	// TTL 内：房间仍被 pending 锁定。
	clock.advance(59 * time.Second)
	if _, err := svc.Reserve(groupReq("NEW1", "2026-10-01", "2026-10-05", "STD", 3)); !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("pending hold err = %v, want ErrInsufficientRooms", err)
	}

	// 超过 TTL：显式推进后旧预留过期、房间释放。
	clock.advance(2 * time.Second)
	n, err := svc.Expire()
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if n != 1 {
		t.Fatalf("expired count = %d, want 1", n)
	}
	if got, _ := svc.GetReservation("OLD"); got.Status != StatusExpired {
		t.Fatalf("OLD status = %s, want expired", got.Status)
	}

	// 迟到确认必须失败，且不能复活占用。
	if _, err := svc.Confirm("OLD"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("late confirm err = %v, want ErrTerminal", err)
	}

	// 释放后的房间可被后来订单使用，并可正常确认。
	newOrder, err := svc.Reserve(groupReq("NEW2", "2026-10-01", "2026-10-05", "STD", 3))
	if err != nil {
		t.Fatalf("Reserve NEW2 after expiry: %v", err)
	}
	if !equalStrings(newOrder.RoomNumbers, old.RoomNumbers) {
		t.Fatalf("NEW2 rooms = %v, want reusing %v", newOrder.RoomNumbers, old.RoomNumbers)
	}
	confirmed, err := svc.Confirm("NEW2")
	if err != nil {
		t.Fatalf("Confirm NEW2: %v", err)
	}
	if confirmed.Status != StatusConfirmed {
		t.Fatalf("NEW2 status = %s", confirmed.Status)
	}

	// 过期推进幂等：再次推进不会重复释放。
	n, err = svc.Expire()
	if err != nil {
		t.Fatalf("second Expire: %v", err)
	}
	if n != 0 {
		t.Fatalf("second expire count = %d, want 0", n)
	}
}

func TestExpireAndConfirmRaceSingleTerminal(t *testing.T) {
	const rounds = 100
	for i := 0; i < rounds; i++ {
		svc, clock := newTestService(t)
		ext := fmt.Sprintf("EXP-%03d", i)
		if _, err := svc.Reserve(groupReq(ext, "2026-12-01", "2026-12-03", "STD", 1)); err != nil {
			t.Fatalf("Reserve %s: %v", ext, err)
		}
		clock.advance(time.Minute + time.Nanosecond)

		var wg sync.WaitGroup
		var mu sync.Mutex
		confirmed, terminal, expired := 0, 0, 0
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := svc.Confirm(ext)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				confirmed++
			case errors.Is(err, ErrTerminal):
				terminal++
			default:
				t.Errorf("Confirm %s: %v", ext, err)
			}
		}()
		go func() {
			defer wg.Done()
			n, err := svc.Expire()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Errorf("Expire: %v", err)
				return
			}
			expired += n
		}()
		wg.Wait()
		if confirmed+terminal != 1 || confirmed+expired != 1 {
			t.Fatalf("round %d: confirmed=%d terminal=%d expired=%d", i, confirmed, terminal, expired)
		}
		final, _ := svc.GetReservation(ext)
		if final.Status != StatusConfirmed && final.Status != StatusExpired {
			t.Fatalf("round %d final status = %s", i, final.Status)
		}
	}
}

func TestCancelReleasesRoomsAndIsTerminal(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Reserve(groupReq("G1", "2026-10-01", "2026-10-04", "STD", 3)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	cancelled, err := svc.Cancel("G1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("status = %s", cancelled.Status)
	}
	// 释放后同样需求可以整组订到。
	retry, err := svc.Reserve(groupReq("G2", "2026-10-01", "2026-10-04", "STD", 3))
	if err != nil {
		t.Fatalf("Reserve after cancel: %v", err)
	}
	if !equalStrings(retry.RoomNumbers, []string{"201", "202", "203"}) {
		t.Fatalf("rooms = %v", retry.RoomNumbers)
	}
	// 终态操作返回 ErrTerminal；重复取消幂等。
	if _, err := svc.Confirm("G1"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("confirm after cancel err = %v, want ErrTerminal", err)
	}
	if _, err := svc.Cancel("G1"); err != nil {
		t.Fatalf("idempotent cancel: %v", err)
	}
	if _, err := svc.Cancel("MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancel missing err = %v, want ErrNotFound", err)
	}
}

func TestCalendarReflectsPendingConfirmedAndReleased(t *testing.T) {
	svc, _ := newTestService(t)
	// pending：10-01..10-03，两晚，两间房。
	r, err := svc.Reserve(groupReq("G1", "2026-10-01", "2026-10-03", "STD", 2))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// confirmed：10-02..10-04。
	if _, err := svc.Reserve(groupReq("G2", "2026-10-02", "2026-10-04", "STD", 2)); err != nil {
		t.Fatalf("Reserve G2: %v", err)
	}
	if _, err := svc.Confirm("G2"); err != nil {
		t.Fatalf("Confirm G2: %v", err)
	}
	// 随后取消：不应出现在日历中。
	if _, err := svc.Reserve(groupReq("G3", "2026-10-01", "2026-10-05", "DLX", 1)); err != nil {
		t.Fatalf("Reserve G3: %v", err)
	}
	if _, err := svc.Cancel("G3"); err != nil {
		t.Fatalf("Cancel G3: %v", err)
	}

	entries, err := svc.Calendar("", MustParseDate("2026-10-01"), MustParseDate("2026-10-05"))
	if err != nil {
		t.Fatalf("Calendar: %v", err)
	}
	counts := map[string]int{}
	statusAt := map[string]Status{}
	for _, e := range entries {
		counts[e.Date.String()]++
		statusAt[e.ExternalOrderID+"@"+e.Date.String()] = e.Status
		if e.RoomNumber == "301" {
			t.Fatalf("cancelled DLX room 301 must not appear in calendar")
		}
	}
	// 10-01: G1 两间；10-02: G1 两间 + G2 两间；10-03: G2 两间。
	if counts["2026-10-01"] != 2 || counts["2026-10-02"] != 4 || counts["2026-10-03"] != 2 {
		t.Fatalf("night counts = %v, want 2/4/2", counts)
	}
	if counts["2026-10-04"] != 0 {
		t.Fatalf("checkout day must be excluded, got %d", counts["2026-10-04"])
	}
	if statusAt["G1@2026-10-01"] != StatusPending {
		t.Fatalf("G1 entry status = %v, want pending", statusAt["G1@2026-10-01"])
	}
	if statusAt["G2@2026-10-02"] != StatusConfirmed {
		t.Fatalf("G2 entry status = %v, want confirmed", statusAt["G2@2026-10-02"])
	}

	// 房型过滤。
	dlx, err := svc.Calendar("DLX", MustParseDate("2026-10-01"), MustParseDate("2026-10-05"))
	if err != nil {
		t.Fatalf("Calendar DLX: %v", err)
	}
	if len(dlx) != 0 {
		t.Fatalf("DLX calendar should be empty, got %d", len(dlx))
	}

	// 排序检查：按日期、楼层、房号。
	if entries[0].Date.String() != "2026-10-01" {
		t.Fatalf("first entry date = %s", entries[0].Date)
	}
	_ = r
}

func TestRoomTypeChangeConflictWhileAllocated(t *testing.T) {
	svc, _ := newTestService(t)
	if _, err := svc.Reserve(groupReq("G1", "2026-10-01", "2026-10-04", "DLX", 1)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	err := svc.UpsertRooms(Room{Number: "301", Floor: 3, RoomType: "STD"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("type change err = %v, want ErrConflict", err)
	}
	if _, err := svc.Cancel("G1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := svc.UpsertRooms(Room{Number: "301", Floor: 3, RoomType: "STD"}); err != nil {
		t.Fatalf("type change after release: %v", err)
	}
}
