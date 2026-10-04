package goroomallocation

import "testing"

func TestChooseRoomsPrefersSameFloorAndCloseNumbers(t *testing.T) {
	// 3、4 楼各有空房；需要 3 间时应集中在 4 楼连续房号。
	rooms := []Room{
		{Number: "301", Floor: 3, RoomType: "STD"},
		{Number: "302", Floor: 3, RoomType: "STD"},
		{Number: "310", Floor: 3, RoomType: "STD"},
		{Number: "401", Floor: 4, RoomType: "STD"},
		{Number: "402", Floor: 4, RoomType: "STD"},
		{Number: "403", Floor: 4, RoomType: "STD"},
		{Number: "410", Floor: 4, RoomType: "STD"},
	}
	got := chooseRooms(rooms, 3)
	want := []string{"401", "402", "403"}
	assertNumbers(t, got, want)

	// 需要 2 间：两层都有同层方案，选低楼层且最紧凑的。
	got = chooseRooms(rooms, 2)
	assertNumbers(t, got, []string{"301", "302"})
}

func TestChooseRoomsCrossFloorFallback(t *testing.T) {
	// 单层不够 3 间时允许跨层，且取楼层跨度最小、房号最紧的窗口。
	rooms := []Room{
		{Number: "101", Floor: 1, RoomType: "STD"},
		{Number: "201", Floor: 2, RoomType: "STD"},
		{Number: "202", Floor: 2, RoomType: "STD"},
		{Number: "501", Floor: 5, RoomType: "STD"},
	}
	got := chooseRooms(rooms, 3)
	assertNumbers(t, got, []string{"101", "201", "202"})
}

func TestChooseRoomsDeterministic(t *testing.T) {
	rooms := []Room{
		{Number: "403", Floor: 4, RoomType: "STD"},
		{Number: "401", Floor: 4, RoomType: "STD"},
		{Number: "402", Floor: 4, RoomType: "STD"},
		{Number: "404", Floor: 4, RoomType: "STD"},
	}
	first := chooseRooms(rooms, 3)
	for i := 0; i < 50; i++ {
		got := chooseRooms(rooms, 3)
		assertNumbers(t, got, roomNumbersOf(first))
	}
	assertNumbers(t, first, []string{"401", "402", "403"})
}

func TestChooseRoomsInsufficient(t *testing.T) {
	rooms := []Room{{Number: "101", Floor: 1, RoomType: "STD"}}
	if got := chooseRooms(rooms, 2); got != nil {
		t.Fatalf("expected nil for insufficient rooms, got %v", got)
	}
	if got := chooseRooms(rooms, 0); got != nil {
		t.Fatalf("expected nil for non-positive count, got %v", got)
	}
}

func TestOverlapsHalfOpen(t *testing.T) {
	d1 := MustParseDate("2026-10-01")
	d2 := MustParseDate("2026-10-03")
	d3 := MustParseDate("2026-10-05")
	if !overlaps(d1, d3, d2, d3.Add(1)) {
		t.Fatal("overlapping intervals should overlap")
	}
	// 首尾相接：[10-01,10-03) 与 [10-03,10-05) 不相交。
	if overlaps(d1, d2, d2, d3) {
		t.Fatal("back-to-back half-open intervals must not overlap")
	}
	// 单晚：[10-01,10-02) 与 [10-02,10-03) 不相交。
	night1 := d1.Add(1)
	night2 := d1.Add(2)
	if overlaps(d1, night1, night1, night2) {
		t.Fatal("one-night back-to-back stays must not overlap")
	}
}

func assertNumbers(t *testing.T, rooms []Room, want []string) {
	t.Helper()
	if len(rooms) != len(want) {
		t.Fatalf("got %d rooms %v, want %v", len(rooms), roomNumbersOf(rooms), want)
	}
	for i, room := range rooms {
		if room.Number != want[i] {
			t.Fatalf("position %d: got %q, want %q (full %v)", i, room.Number, want[i], roomNumbersOf(rooms))
		}
	}
}

func roomNumbersOf(rooms []Room) []string {
	out := make([]string, len(rooms))
	for i, room := range rooms {
		out[i] = room.Number
	}
	return out
}
