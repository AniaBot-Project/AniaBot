// 主动互动的「活跃度」滑条 → 具体节奏参数。
//
// 这些节奏原先分别是 8 个配置项（冷场阈值、最小间隔、每日上限、全局上限、
// 有效期、观察窗口、冷却、连续无人理次数），现在只由 0~100 的活跃度在
// 5 个锚点之间线性插值得到。50 分（均衡）等于历史默认值，升级后不调滑条
// 即为原来的节奏。
package pluginaichat

import "math"

// proactiveDefaultActivity 未配置时使用的活跃度（均衡）。
const proactiveDefaultActivity = 50

// proactiveActivityPreset 一个活跃度档位对应的节奏参数。
type proactiveActivityPreset struct {
	Activity         int    // 换算后的活跃度（0~100，已限幅）
	Name             string // 档位名，用于 /proactive 与日志
	IdleMinutes      int    // 冷场多久后生成意图
	MinGapMinutes    int    // 同一会话两次主动发言的最小间隔
	DailyLimit       int    // 单会话每日上限
	GlobalDailyLimit int    // 全局每日上限
	ExpireMinutes    int    // 意图有效期
	OutcomeMinutes   int    // 回应观察窗口
	CooldownMinutes  int    // 连续无人理后的冷却
	StrikeLimit      int    // 连续多少次无人理后进入冷却
}

// proactiveActivityAnchors 滑条的 5 个锚点（0/25/50/75/100），
// 中间的取值在相邻锚点之间线性插值。
var proactiveActivityAnchors = []proactiveActivityPreset{
	{
		Activity: 0, Name: "冷淡",
		IdleMinutes: 240, MinGapMinutes: 240, DailyLimit: 1, GlobalDailyLimit: 6,
		ExpireMinutes: 45, OutcomeMinutes: 15, CooldownMinutes: 720, StrikeLimit: 2,
	},
	{
		Activity: 25, Name: "克制",
		IdleMinutes: 150, MinGapMinutes: 120, DailyLimit: 2, GlobalDailyLimit: 12,
		ExpireMinutes: 40, OutcomeMinutes: 12, CooldownMinutes: 600, StrikeLimit: 3,
	},
	{
		Activity: 50, Name: "均衡",
		IdleMinutes: 90, MinGapMinutes: 60, DailyLimit: 3, GlobalDailyLimit: 20,
		ExpireMinutes: 30, OutcomeMinutes: 10, CooldownMinutes: 360, StrikeLimit: 3,
	},
	{
		Activity: 75, Name: "活跃",
		IdleMinutes: 45, MinGapMinutes: 30, DailyLimit: 5, GlobalDailyLimit: 40,
		ExpireMinutes: 20, OutcomeMinutes: 8, CooldownMinutes: 180, StrikeLimit: 4,
	},
	{
		Activity: 100, Name: "话痨",
		IdleMinutes: 20, MinGapMinutes: 15, DailyLimit: 8, GlobalDailyLimit: 80,
		ExpireMinutes: 15, OutcomeMinutes: 5, CooldownMinutes: 120, StrikeLimit: 6,
	},
}

// proactiveActivityPresetFor 把 0~100 的活跃度换算成具体节奏参数。
// 越靠「活跃」：冷场阈值与最小间隔越短，每日上限越高，冷却越短；
// 中间取值线性插值（取整），档位名取更近的锚点。
func proactiveActivityPresetFor(activity int) proactiveActivityPreset {
	activity = min(max(activity, 0), 100)

	first, last := proactiveActivityAnchors[0], proactiveActivityAnchors[len(proactiveActivityAnchors)-1]
	if activity <= first.Activity {
		first.Activity = activity
		return first
	}
	if activity >= last.Activity {
		last.Activity = activity
		return last
	}
	for i := 1; i < len(proactiveActivityAnchors); i++ {
		hi := proactiveActivityAnchors[i]
		if activity > hi.Activity {
			continue
		}
		lo := proactiveActivityAnchors[i-1]
		t := float64(activity-lo.Activity) / float64(hi.Activity-lo.Activity)
		name := lo.Name
		if t >= 0.5 {
			name = hi.Name
		}
		return proactiveActivityPreset{
			Activity:         activity,
			Name:             name,
			IdleMinutes:      lerpInt(lo.IdleMinutes, hi.IdleMinutes, t),
			MinGapMinutes:    lerpInt(lo.MinGapMinutes, hi.MinGapMinutes, t),
			DailyLimit:       max(lerpInt(lo.DailyLimit, hi.DailyLimit, t), 1),
			GlobalDailyLimit: max(lerpInt(lo.GlobalDailyLimit, hi.GlobalDailyLimit, t), 1),
			ExpireMinutes:    max(lerpInt(lo.ExpireMinutes, hi.ExpireMinutes, t), 1),
			OutcomeMinutes:   max(lerpInt(lo.OutcomeMinutes, hi.OutcomeMinutes, t), 1),
			CooldownMinutes:  max(lerpInt(lo.CooldownMinutes, hi.CooldownMinutes, t), 1),
			StrikeLimit:      max(lerpInt(lo.StrikeLimit, hi.StrikeLimit, t), 0),
		}
	}
	// 理论上不可达（上面的区间已覆盖 0~100），兜底返回均衡档。
	fallback := proactiveActivityAnchors[2]
	fallback.Activity = activity
	return fallback
}

// lerpInt 在 a、b 之间按 t（0~1）线性插值并四舍五入。
func lerpInt(a, b int, t float64) int {
	return int(math.Round(float64(a) + float64(b-a)*t))
}
