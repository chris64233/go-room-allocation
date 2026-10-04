# go-room-allocation

酒店团体订单整组房间预留服务（Go）。

团体订单给出左闭右开的入住区间 `[CheckIn, CheckOut)`、房型与房间数；
系统在整个入住区间内挑选都可用的房间，按“楼层集中、房号接近”给出
确定方案，数量不足时整单失败（不会部分占用）。

## 功能

- **房间配置**：`UpsertRooms` / `Rooms`，房号唯一；已被有效预留占用的
  房间不允许变更房型。
- **团体预留** `Reserve`：在整个入住区间都可用才入选；滑窗算法优先
  楼层跨度最小、其次房号跨度最小，结果完全确定。数量不足返回
  `ErrInsufficientRooms`，且不落库任何分配。
- **限时确认**：预留进入 `pending` 并锁定房间，TTL 内可 `Confirm`；
  可随时 `Cancel` 主动释放；超时由后台 janitor 或任意调用路径惰性推进为
  `expired`。终态（confirmed / cancelled / expired）只能落一次，
  占用只建立或释放一次。
- **幂等与冲突**：相同外部订单号重复提交相同需求返回原方案；改变日期、
  房型或数量返回 `ErrConflict`。幂等性在并发与进程重启后都成立。
- **并发安全**：所有分配都在存储层单事务
  （查可用房 → 选房 → 写预留）内完成，多个团体并发争房不会重复分配；
  确认 / 取消 / 超时同时发生只有一个终态胜出，迟到确认被 `ErrTerminal`
  拒绝，不能影响已释放房间的后续订单。
- **入住日历** `Calendar`：按晚查询每间房的 `pending` / `confirmed`
  占用（退房当日不计），支持房型过滤。
- **持久化**：`FileStore` 将房间配置与全部预留（分配依据）以 JSON 原子
  写入磁盘（临时文件 + rename），重启后占用与幂等记录完整恢复；
  另提供 `MemoryStore`。

## 快速开始

```go
svc, err := goroomallocation.NewFileService("allocation.json", 15*time.Minute)
if err != nil {
    log.Fatal(err)
}
defer svc.Close()

err = svc.UpsertRooms(
    goroomallocation.Room{Number: "801", Floor: 8, RoomType: "DLX"},
    goroomallocation.Room{Number: "802", Floor: 8, RoomType: "DLX"},
    goroomallocation.Room{Number: "803", Floor: 8, RoomType: "DLX"},
)

checkIn, _ := goroomallocation.ParseDate("2026-10-01")
checkOut, _ := goroomallocation.ParseDate("2026-10-04") // 不含，共 3 晚
res, err := svc.Reserve(goroomallocation.Request{
    ExternalOrderID: "TEAM-2026-001",
    CheckIn:         checkIn,
    CheckOut:        checkOut,
    RoomType:        "DLX",
    Count:           3,
})
// res.RoomNumbers 为整组方案，例如 [801 802 803]

if _, err := svc.Confirm("TEAM-2026-001"); err != nil {
    log.Fatal(err) // 超时或已取消时返回 ErrTerminal
}

entries, _ := svc.Calendar("DLX", checkIn, checkOut) // 每晚占用明细
```

默认仅在调用路径上惰性推进过期；如需后台扫描：

```go
svc, _ := goroomallocation.NewFileService(
    "allocation.json",
    15*time.Minute,
    goroomallocation.WithSweepInterval(time.Minute),
)
```

测试中可注入 `WithClock(clock)` 控制超时。

## 错误

| 错误 | 含义 |
| --- | --- |
| `ErrInvalidArgument` | 请求参数非法（空单号、区间倒置、数量非正等） |
| `ErrInsufficientRooms` | 整段区间内可用房间不足，整单失败 |
| `ErrConflict` | 外单号重复但需求变更，或占用中房间变更房型 |
| `ErrTerminal` | 预留已处于终态（含过期后的迟到确认） |
| `ErrNotFound` | 外单号或房间不存在 |

## 状态机

```
pending ──Confirm──▶ confirmed
   │
   ├──Cancel──▶ cancelled
   │
   └──TTL 到期─▶ expired
```

`pending` 与 `confirmed` 都持有房间占用；`cancelled` 与 `expired`
立即释放。终态之间不可流转，重复进入同一终态按幂等返回原方案。

## 测试

```bash
go test -race -count=1 ./...
```

覆盖选房确定性、左闭右开区间、整单失败不入库、幂等与冲突、并发争房
（50 goroutine / 单外单号并发）、确认-取消-超时竞态（各 100 轮）、
迟到确认、日历、文件持久化重启恢复与后台 janitor。
