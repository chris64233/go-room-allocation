package goroomallocation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRequestValidation(t *testing.T) {
	base := groupReq("G1", "2026-10-01", "2026-10-02", "STD", 1)
	cases := []Request{
		{CheckIn: base.CheckIn, CheckOut: base.CheckOut, RoomType: "STD", Count: 1},
		{ExternalOrderID: "G1", CheckOut: base.CheckOut, RoomType: "STD", Count: 1},
		{ExternalOrderID: "G1", CheckIn: base.CheckIn, RoomType: "STD", Count: 1},
		{ExternalOrderID: "G1", CheckIn: base.CheckIn, CheckOut: base.CheckIn, RoomType: "STD", Count: 1},
		{ExternalOrderID: "G1", CheckIn: base.CheckOut, CheckOut: base.CheckIn, RoomType: "STD", Count: 1},
		{ExternalOrderID: "G1", CheckIn: base.CheckIn, CheckOut: base.CheckOut, Count: 1},
		{ExternalOrderID: "G1", CheckIn: base.CheckIn, CheckOut: base.CheckOut, RoomType: "STD", Count: 0},
	}
	svc, _ := newTestService(t)
	for i, req := range cases {
		if _, err := svc.Reserve(req); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("case %d: err = %v, want ErrInvalidArgument", i, err)
		}
	}
	if _, err := svc.Confirm(""); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Confirm empty id err = %v, want ErrInvalidArgument", err)
	}
	if _, err := svc.GetReservation("MISSING"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReservation missing err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Calendar("STD", base.CheckOut, base.CheckIn); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("Calendar bad range err = %v, want ErrInvalidArgument", err)
	}
}

func TestRoomValidationAndUpsert(t *testing.T) {
	svc, _ := newTestService(t)
	bad := []Room{
		{Number: "", Floor: 1, RoomType: "STD"},
		{Number: "X", Floor: -1, RoomType: "STD"},
		{Number: "X", Floor: 1, RoomType: ""},
	}
	for i, room := range bad {
		if err := svc.UpsertRooms(room); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("case %d: err = %v, want ErrInvalidArgument", i, err)
		}
	}
	if err := svc.UpsertRooms(Room{Number: "901", Floor: 9, RoomType: "STE"}); err != nil {
		t.Fatalf("UpsertRooms: %v", err)
	}
	rooms, err := svc.Rooms()
	if err != nil {
		t.Fatalf("Rooms: %v", err)
	}
	if len(rooms) != 7 || rooms[6].Number != "901" {
		t.Fatalf("rooms = %v", rooms)
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(nil, time.Minute); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("nil store err = %v", err)
	}
	if _, err := NewMemoryService(0); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero ttl err = %v", err)
	}
	if _, err := NewMemoryService(-time.Second); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("negative ttl err = %v", err)
	}
}

func TestFileStoreErrorsAndIdempotentClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "allocation.json")
	if _, err := NewFileStore(path); err == nil {
		t.Fatal("expected error when parent directory does not exist")
	}
	good := filepath.Join(dir, "allocation.json")
	store, err := NewFileStore(good)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc, err := NewService(store, time.Minute)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := svc.Close(); err != nil {
		t.Fatalf("idempotent Close: %v", err)
	}

	// 写入坏数据后加载应报错。
	if err := os.WriteFile(good, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := NewFileStore(good); err == nil {
		t.Fatal("expected error loading corrupt file")
	}
}

func TestDateHelpers(t *testing.T) {
	d := MustParseDate("2026-10-01")
	if d.Add(2).String() != "2026-10-03" {
		t.Fatalf("Add = %s", d.Add(2))
	}
	if d.Nights(MustParseDate("2026-10-04")) != 3 {
		t.Fatal("Nights mismatch")
	}
	if d.Nights(d) != 0 {
		t.Fatal("same-date nights should be 0")
	}
	text, err := d.MarshalText()
	if err != nil || string(text) != "2026-10-01" {
		t.Fatalf("MarshalText = %q, %v", text, err)
	}
	var back Date
	if err := back.UnmarshalText([]byte("2026-10-01")); err != nil || !back.Equal(d) {
		t.Fatalf("UnmarshalText = %v, %v", back, err)
	}
	if _, err := ParseDate("bad"); err == nil {
		t.Fatal("ParseDate bad input should fail")
	}
	if NewDate(time.Date(2026, 10, 1, 23, 59, 0, 0, time.FixedZone("X", 5*3600))).String() != "2026-10-01" {
		t.Fatal("NewDate should truncate to day in source zone")
	}
}
