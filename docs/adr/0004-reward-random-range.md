# 0004 奖励额度按区间随机，对齐 new-api 的签到口径

签到奖励不是固定值，而是一个 **`[min, max]` 区间**，每天为每个用户在该区间内随机取一个值。

原因：new-api 自己的网页签到就是这个语义（`setting/operation_setting/checkin_setting.go`
的 `MinQuota`/`MaxQuota`，`model/checkin.go` 里 `min + rand.Intn(max-min+1)`）。本项目用
同一口径，管理员在两端看到的概念一致，将来若要把本项目并入 new-api 的签到体系也不用改语义。

约定：
- 区间可配置，`max` 必须 ≥ `min`；`max == min` 即固定值。
- 随机值**每天每用户取一次**，取到即发、发完落库；重试或重跑当天不得再取新值（否则
  「已发」守卫形同虚设）。
- 额度单位是 new-api 的 quota（该实例 `quota_per_unit = 500000`，即 500000 quota = $1）。
  测试期区间设为 **100–200**，对应 $0.0002–$0.0004。
