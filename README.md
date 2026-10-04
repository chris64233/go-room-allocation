# go-room-allocation

本项目用于 GSB 评测，实现酒店团体订单的整组房间预留，核心代码在 `hotel/` 包。

## 功能

- **房间配置**：`AddRoom` 登记客房（房号、楼层、房型），`Rooms` 查询配置。
- **团体预留**：`Reserve` 按左闭右开区间 `[CheckIn, CheckOut)` 选择整个入住期内都可用的房间，
  按“楼层集中、房号接近”给出确定方案；数量不足时整单失败（`ErrInsufficientRooms`），不留部分占用。
- **幂等与冲突**：相同外部订单号重复提交相同需求返回原方案；改变日期或数量返回 `ErrConflict`。
- **生命周期**：预留在 `TTL` 内等待确认。`Confirm` / `Cancel` / `ExpirePending`（过期推进）
  三者只有一个终态生效，房间占用只建立一次、释放一次；旧预留的迟到确认返回错误，
  不影响后来复用这些房间的订单。
- **入住日历**：`Calendar(from, to)` 返回每日每房的占用情况及各房型剩余间数。
- **持久化**：每次状态变更把分配依据（需求、房号、时限、状态）快照写入 `FileStore`
  （JSON 文件，临时文件 + 原子重命名），重启后自动恢复占用与幂等记录。

## 并发语义

`Hotel` 内部以单互斥锁串行化所有状态变更：分房、确认、取消、过期推进都是原子操作，
多个团体并发争用同一批房间时不会重复分配（见 `TestConcurrentReservesNeverDoubleAllocate`）。

## 使用示例

```go
store := hotel.NewFileStore("data/hotel-snapshot.json")
h, _ := hotel.New(store) // 可选 hotel.WithClock(...) 注入时钟

h.AddRoom(hotel.Room{Number: 801, Floor: 8, Type: "std"})

res, err := h.Reserve(hotel.ReserveRequest{
    ExternalOrderID: "G-1001",
    RoomType:        "std",
    CheckIn:         hotel.NewDate(2026, time.October, 1),
    CheckOut:        hotel.NewDate(2026, time.October, 3), // 住 10-01、10-02 两晚
    Count:           3,
    TTL:             30 * time.Minute,
})

err = h.Confirm(res.ID)   // 或 h.Cancel(res.ID)
n := h.ExpirePending()    // 推进过期，释放超时未确认的房间
days, _ := h.Calendar(hotel.NewDate(2026, time.October, 1), hotel.NewDate(2026, time.October, 4))
```

## 测试

```sh
go test ./...          # 单元测试
go test ./hotel/ -race # 含并发竞态检测
```
