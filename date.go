package goroomallocation

import "time"

// DateLayout 是日期使用的标准格式（公历 YYYY-MM-DD）。
const DateLayout = "2006-01-02"

// Date 表示一个当地日期，精度到天，不携带时区。
// 入住区间按左闭右开 [CheckIn, CheckOut) 处理。
type Date struct {
	t time.Time
}

// NewDate 由任意 time.Time（按其所在地时区）截取日期部分构造 Date。
func NewDate(t time.Time) Date {
	y, m, d := t.Date()
	return Date{t: time.Date(y, m, d, 0, 0, 0, 0, time.UTC)}
}

// ParseDate 按 "YYYY-MM-DD" 解析日期。
func ParseDate(s string) (Date, error) {
	t, err := time.Parse(DateLayout, s)
	if err != nil {
		return Date{}, err
	}
	return Date{t: t}, nil
}

// MustParseDate 与 ParseDate 相同，但在解析失败时 panic。
func MustParseDate(s string) Date {
	d, err := ParseDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Add 返回该日期加上 n 天之后的日期。
func (d Date) Add(n int) Date {
	return Date{t: d.t.AddDate(0, 0, n)}
}

// Nights 返回入住区间 [d, other) 的晚数；other 不晚于 d 时返回 0。
func (d Date) Nights(other Date) int {
	n := int(other.t.Sub(d.t).Hours() / 24)
	if n < 0 {
		return 0
	}
	return n
}

// Before 报告 d 是否早于 other。
func (d Date) Before(other Date) bool { return d.t.Before(other.t) }

// Equal 报告两个日期是否为同一天。
func (d Date) Equal(other Date) bool { return d.t.Equal(other.t) }

// IsZero 报告日期是否为零值。
func (d Date) IsZero() bool { return d.t.IsZero() }

// String 返回 "YYYY-MM-DD" 形式。
func (d Date) String() string { return d.t.Format(DateLayout) }

// Time 转换为 UTC 零点的 time.Time。
func (d Date) Time() time.Time { return d.t }

// MarshalText 实现 encoding.TextMarshaler。
func (d Date) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

// UnmarshalText 实现 encoding.TextUnmarshaler。
func (d *Date) UnmarshalText(data []byte) error {
	parsed, err := ParseDate(string(data))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// overlaps 报告左闭右开区间 [aStart, aEnd) 与 [bStart, bEnd) 是否有交集。
func overlaps(aStart, aEnd, bStart, bEnd Date) bool {
	return aStart.Before(bEnd) && bStart.Before(aEnd)
}
