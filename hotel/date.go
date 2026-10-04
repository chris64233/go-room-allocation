package hotel

import (
	"encoding/json"
	"fmt"
	"time"
)

// Date 表示一个日历日，内部统一为 UTC 零点，可比较、可作 map 键。
type Date struct {
	t time.Time
}

// NewDate 按年月日构造日历日。
func NewDate(year int, month time.Month, day int) Date {
	return Date{t: time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// DateOf 取某个时刻所在的日历日（按该时刻的时区）。
func DateOf(t time.Time) Date {
	y, m, d := t.Date()
	return NewDate(y, m, d)
}

func (d Date) Before(o Date) bool { return d.t.Before(o.t) }
func (d Date) After(o Date) bool  { return d.t.After(o.t) }
func (d Date) Equal(o Date) bool  { return d.t.Equal(o.t) }

// Next 返回下一天。
func (d Date) Next() Date { return Date{t: d.t.AddDate(0, 0, 1)} }

// DaysUntil 返回 d 到 o 之间的天数（o 早于 d 时为负）。
func (d Date) DaysUntil(o Date) int { return int(o.t.Sub(d.t).Hours() / 24) }

func (d Date) String() string { return d.t.Format("2006-01-02") }

// MarshalJSON 以 "2006-01-02" 形式序列化。
func (d Date) MarshalJSON() ([]byte, error) {
	return fmt.Appendf(nil, "%q", d.String()), nil
}

// UnmarshalJSON 解析 "2006-01-02"。
func (d *Date) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return fmt.Errorf("hotel: invalid date %q: %w", s, err)
	}
	d.t = t
	return nil
}

// EachNight 按左闭右开展开入住区间 [checkIn, checkOut)，返回每一晚的日期。
func EachNight(checkIn, checkOut Date) []Date {
	var nights []Date
	for d := checkIn; d.Before(checkOut); d = d.Next() {
		nights = append(nights, d)
	}
	return nights
}
