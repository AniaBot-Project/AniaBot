package pluginaichat

import "testing"

// TestActivityPresetAnchors 锚点原样返回，且「均衡」（默认值）必须等于历史默认节奏：
// 升级后不动滑条，行为与逐项配置的时代一致。
func TestActivityPresetAnchors(t *testing.T) {
	for _, want := range proactiveActivityAnchors {
		if got := proactiveActivityPresetFor(want.Activity); got != want {
			t.Errorf("activity=%d: got %+v, want %+v", want.Activity, got, want)
		}
	}

	balanced := proactiveActivityPresetFor(proactiveDefaultActivity)
	want := proactiveActivityPreset{
		Activity: 50, Name: "均衡",
		IdleMinutes: 90, MinGapMinutes: 60, DailyLimit: 3, GlobalDailyLimit: 20,
		ExpireMinutes: 30, OutcomeMinutes: 10, CooldownMinutes: 360, StrikeLimit: 3,
	}
	if balanced != want {
		t.Fatalf("默认活跃度应保持历史默认值: got %+v, want %+v", balanced, want)
	}
}

// TestActivityPresetMonotonic 越活跃：冷场阈值与最小间隔越短、上限越高、
// 冷却越短；任何取值下都必须落在合理范围（不会出现 0 次上限这类死配置）。
func TestActivityPresetMonotonic(t *testing.T) {
	prev := proactiveActivityPresetFor(0)
	for activity := 1; activity <= 100; activity++ {
		cur := proactiveActivityPresetFor(activity)
		if cur.Activity != activity {
			t.Fatalf("activity=%d: preset.Activity = %d", activity, cur.Activity)
		}
		if cur.IdleMinutes > prev.IdleMinutes || cur.MinGapMinutes > prev.MinGapMinutes {
			t.Fatalf("activity=%d: 冷场/间隔应随活跃度递减: prev %+v cur %+v", activity, prev, cur)
		}
		if cur.DailyLimit < prev.DailyLimit || cur.GlobalDailyLimit < prev.GlobalDailyLimit {
			t.Fatalf("activity=%d: 每日上限应随活跃度递增: prev %+v cur %+v", activity, prev, cur)
		}
		if cur.ExpireMinutes > prev.ExpireMinutes || cur.CooldownMinutes > prev.CooldownMinutes {
			t.Fatalf("activity=%d: 有效期/冷却应随活跃度递减: prev %+v cur %+v", activity, prev, cur)
		}
		if cur.IdleMinutes < 1 || cur.MinGapMinutes < 1 || cur.DailyLimit < 1 || cur.GlobalDailyLimit < 1 {
			t.Fatalf("activity=%d: 参数越界: %+v", activity, cur)
		}
		if cur.ExpireMinutes < 1 || cur.OutcomeMinutes < 1 || cur.CooldownMinutes < 1 || cur.StrikeLimit < 0 {
			t.Fatalf("activity=%d: 参数越界: %+v", activity, cur)
		}
		prev = cur
	}
}

// TestActivityPresetClamp 越界的滑条值（配置被手工改写）按两端处理。
func TestActivityPresetClamp(t *testing.T) {
	low := proactiveActivityPresetFor(-20)
	if low.Activity != 0 || low.Name != "冷淡" || low != proactiveActivityAnchors[0] {
		t.Errorf("负值应夹到冷淡档: %+v", low)
	}
	high := proactiveActivityPresetFor(180)
	if high.Activity != 100 || high.Name != "话痨" || high != proactiveActivityAnchors[len(proactiveActivityAnchors)-1] {
		t.Errorf("超过 100 应夹到话痨档: %+v", high)
	}
}

// TestActivityPresetInterpolate 中间取值在相邻锚点之间线性插值，档位名取更近的锚点。
func TestActivityPresetInterpolate(t *testing.T) {
	got := proactiveActivityPresetFor(60)
	want := proactiveActivityPreset{
		Activity: 60, Name: "均衡", // 距 50 更近
		IdleMinutes: 72, MinGapMinutes: 48, DailyLimit: 4, GlobalDailyLimit: 28,
		ExpireMinutes: 26, OutcomeMinutes: 9, CooldownMinutes: 288, StrikeLimit: 3,
	}
	if got != want {
		t.Fatalf("activity=60: got %+v, want %+v", got, want)
	}
	if name := proactiveActivityPresetFor(63).Name; name != "活跃" {
		t.Errorf("activity=63 应取更近的活跃档, got %q", name)
	}
}
