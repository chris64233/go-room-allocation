package goroomallocation

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStorePersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allocation.json")
	clock := newFakeClock()

	svc, err := NewFileService(path, time.Minute, WithClock(clock))
	if err != nil {
		t.Fatalf("NewFileService: %v", err)
	}
	if err := svc.UpsertRooms(
		Room{Number: "101", Floor: 1, RoomType: "STD"},
		Room{Number: "102", Floor: 1, RoomType: "STD"},
		Room{Number: "201", Floor: 2, RoomType: "STD"},
	); err != nil {
		t.Fatalf("UpsertRooms: %v", err)
	}
	first, err := svc.Reserve(groupReq("P1", "2026-10-01", "2026-10-03", "STD", 2))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := svc.Confirm("P1"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 用同一文件重新打开：配置与已确认占用必须恢复。
	svc2, err := NewFileService(path, time.Minute, WithClock(clock))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = svc2.Close() })

	rooms, err := svc2.Rooms()
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(rooms) != 3 {
		t.Fatalf("rooms after reopen = %d, want 3", len(rooms))
	}
	got, err := svc2.GetReservation("P1")
	if err != nil {
		t.Fatalf("GetReservation: %v", err)
	}
	if got.ID != first.ID || got.Status != StatusConfirmed {
		t.Fatalf("reloaded reservation = %+v, want confirmed %s", got, first.ID)
	}

	// 已确认占用在重启后仍然有效：只剩 1 间可订，订 2 间失败。
	if _, err := svc2.Reserve(groupReq("P2", "2026-10-01", "2026-10-03", "STD", 2)); !errors.Is(err, ErrInsufficientRooms) {
		t.Fatalf("post-restart reserve err = %v, want ErrInsufficientRooms", err)
	}

	// 幂等性在重启后保持。
	again, err := svc2.Reserve(groupReq("P1", "2026-10-01", "2026-10-03", "STD", 2))
	if err != nil {
		t.Fatalf("idempotency after restart: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("id = %s, want %s", again.ID, first.ID)
	}
}

func TestExpiredPendingDoesNotBlockAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allocation.json")
	clock := newFakeClock()
	svc, err := NewFileService(path, time.Minute, WithClock(clock))
	if err != nil {
		t.Fatalf("NewFileService: %v", err)
	}
	if err := svc.UpsertRooms(Room{Number: "101", Floor: 1, RoomType: "STD"}); err != nil {
		t.Fatalf("UpsertRooms: %v", err)
	}
	if _, err := svc.Reserve(groupReq("E1", "2026-10-01", "2026-10-03", "STD", 1)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	clock.advance(2 * time.Minute)
	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	svc2, err := NewFileService(path, time.Minute, WithClock(clock))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = svc2.Close() })
	// 重启后的任何调用都会惰性推进过期，房间可重新预订。
	r, err := svc2.Reserve(groupReq("E2", "2026-10-01", "2026-10-03", "STD", 1))
	if err != nil {
		t.Fatalf("reserve after restart past ttl: %v", err)
	}
	if !equalStrings(r.RoomNumbers, []string{"101"}) {
		t.Fatalf("rooms = %v, want [101]", r.RoomNumbers)
	}
}

func TestJanitorExpiresInBackground(t *testing.T) {
	clock := newFakeClock()
	svc, err := NewMemoryService(time.Minute, WithClock(clock), WithSweepInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewMemoryService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if err := svc.UpsertRooms(Room{Number: "101", Floor: 1, RoomType: "STD"}); err != nil {
		t.Fatalf("UpsertRooms: %v", err)
	}
	if _, err := svc.Reserve(groupReq("J1", "2026-10-01", "2026-10-03", "STD", 1)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	clock.advance(2 * time.Minute)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := svc.GetReservation("J1")
		if got.Status == StatusExpired {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("janitor did not expire the reservation in time")
}
