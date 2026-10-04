package goroomallocation

import "errors"

var (
	// ErrNotFound 在指定房间或预留不存在时返回。
	ErrNotFound = errors.New("goroomallocation: not found")
	// ErrInvalidArgument 在请求参数非法时返回。
	ErrInvalidArgument = errors.New("goroomallocation: invalid argument")
	// ErrConflict 在外单号重复但需求变更、或房间类型变更时返回。
	ErrConflict = errors.New("goroomallocation: conflict")
	// ErrInsufficientRooms 表示整段入住区间内可满足要求的房间数不足，整单失败。
	ErrInsufficientRooms = errors.New("goroomallocation: insufficient rooms")
	// ErrTerminal 表示预留已处于终态（已确认 / 已取消 / 已过期），操作被拒绝。
	ErrTerminal = errors.New("goroomallocation: reservation already terminal")
)
