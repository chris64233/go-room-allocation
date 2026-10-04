// Package goroomallocation 实现酒店团体订单的整组房间预留：
// 在左闭右开的入住区间内选择全程可用、楼层集中且房号接近的房间，
// 支持限时确认、主动取消、超时释放、外单号幂等以及入住日历查询，
// 并通过内存或 JSON 文件存储持久化分配依据。
package goroomallocation
