package goroomallocation

import (
	"fmt"
	"sort"
	"strconv"
)

// Room 表示一间可售客房。
type Room struct {
	// Number 是房间的业务标识（如 "808"），全局唯一。
	Number string `json:"number"`
	// Floor 是房间所在楼层，数值越小楼层越低。
	Floor int `json:"floor"`
	// RoomType 是房型标识（如 "STD"、"DLX"）。
	RoomType string `json:"room_type"`
}

func (r Room) validate() error {
	if r.Number == "" {
		return fmt.Errorf("%w: room number is empty", ErrInvalidArgument)
	}
	if r.Floor < 0 {
		return fmt.Errorf("%w: room floor must be non-negative", ErrInvalidArgument)
	}
	if r.RoomType == "" {
		return fmt.Errorf("%w: room type is empty", ErrInvalidArgument)
	}
	return nil
}

// chooseRooms 在候选房间中按“楼层集中、房号接近”挑选 count 间，
// 并返回确定性的方案。调用方必须保证候选房间都在整段入住区间内可用。
//
// 规则：
//  1. 先按楼层升序、同房层按房号升序排序；
//  2. 在长度为 count 的连续滑窗中，优先选择楼层跨度最小的方案，
//     即尽量集中在同一楼层；
//  3. 楼层跨度相同时选择房号跨度最小的方案（房号可按整数解析时按数值
//     比较，否则按字典序比较）；
//  4. 仍然相同则取首间房楼层、房号最小的窗口，保证结果确定。
func chooseRooms(rooms []Room, count int) []Room {
	if count <= 0 || count > len(rooms) {
		return nil
	}
	sorted := make([]Room, len(rooms))
	copy(sorted, rooms)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Floor != sorted[j].Floor {
			return sorted[i].Floor < sorted[j].Floor
		}
		return sorted[i].Number < sorted[j].Number
	})

	best := -1
	bestScore := windowScore{}
	for start := 0; start+count <= len(sorted); start++ {
		score := scoreWindow(sorted[start : start+count])
		if best == -1 || score.less(bestScore) {
			best = start
			bestScore = score
		}
	}

	result := make([]Room, count)
	copy(result, sorted[best:best+count])
	return result
}

type windowScore struct {
	floorSpan   int // 最高楼层 - 最低楼层
	numSpan     int // 首末房号的数值跨度
	numParsed   bool
	firstFloor  int
	firstNum    int
	firstParsed bool
	firstLex    string
}

func scoreWindow(rooms []Room) windowScore {
	minFloor, maxFloor := rooms[0].Floor, rooms[0].Floor
	for _, r := range rooms[1:] {
		if r.Floor < minFloor {
			minFloor = r.Floor
		}
		if r.Floor > maxFloor {
			maxFloor = r.Floor
		}
	}
	first, last := rooms[0].Number, rooms[len(rooms)-1].Number
	score := windowScore{floorSpan: maxFloor - minFloor, firstFloor: rooms[0].Floor, firstLex: first}
	if fi, fok := parseRoomNumber(first); fok {
		score.firstNum = fi
		score.firstParsed = true
		if li, lok := parseRoomNumber(last); lok {
			score.numSpan = li - fi
			score.numParsed = true
		}
	}
	return score
}

func (s windowScore) less(other windowScore) bool {
	if s.floorSpan != other.floorSpan {
		return s.floorSpan < other.floorSpan
	}
	if s.numParsed && other.numParsed && s.numSpan != other.numSpan {
		return s.numSpan < other.numSpan
	}
	if s.firstFloor != other.firstFloor {
		return s.firstFloor < other.firstFloor
	}
	if s.firstParsed && other.firstParsed && s.firstNum != other.firstNum {
		return s.firstNum < other.firstNum
	}
	return s.firstLex < other.firstLex
}

func parseRoomNumber(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
