package configstore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestMigrateAdminIDsFromLegacy(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set(LegacyAdminIDKey, "qq:111"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateAdminIDs(); err != nil {
		t.Fatal(err)
	}

	got, ok := s.Get(AdminIDsKey)
	if !ok {
		t.Fatal("迁移后应写入 bot.admin_ids")
	}
	if want := []any{"qq:111"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bot.admin_ids = %#v, want %#v", got, want)
	}
	if _, ok := s.Get(LegacyAdminIDKey); ok {
		t.Fatal("迁移后应删除旧键 bot.admin_id")
	}

	// 幂等：再次执行不报错、不改写
	if err := s.MigrateAdminIDs(); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(AdminIDsKey)
	if want := []any{"qq:111"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("重复迁移后 bot.admin_ids = %#v, want %#v", got, want)
	}
}

func TestMigrateAdminIDsMergesExisting(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set(AdminIDsKey, []string{"qq:111"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(LegacyAdminIDKey, "qq:222"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateAdminIDs(); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(AdminIDsKey)
	if want := []any{"qq:111", "qq:222"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bot.admin_ids = %#v, want %#v", got, want)
	}

	// 旧值已存在于列表时不重复追加
	s2 := newTestStore(t)
	if err := s2.Set(AdminIDsKey, []string{"qq:111", "qq:222"}); err != nil {
		t.Fatal(err)
	}
	if err := s2.Set(LegacyAdminIDKey, "qq:222"); err != nil {
		t.Fatal(err)
	}
	if err := s2.MigrateAdminIDs(); err != nil {
		t.Fatal(err)
	}
	got, _ = s2.Get(AdminIDsKey)
	if want := []any{"qq:111", "qq:222"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("去重后 bot.admin_ids = %#v, want %#v", got, want)
	}
}

func TestMigrateAdminIDsEmptyLegacy(t *testing.T) {
	s := newTestStore(t)
	if err := s.Set(LegacyAdminIDKey, "  "); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateAdminIDs(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get(AdminIDsKey); ok {
		t.Fatal("旧值为空时不应写入空的 bot.admin_ids")
	}
	if _, ok := s.Get(LegacyAdminIDKey); ok {
		t.Fatal("迁移后应删除旧键 bot.admin_id")
	}
}

func TestApplyPresetLegacyAdminID(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	p := Preset{Name: "旧预设", CreatedAt: now, UpdatedAt: now, Config: map[string]any{
		LegacyAdminIDKey: "qq:333",
	}}
	if ok := s.presets.Set(context.Background(), "旧预设", p); !ok {
		t.Fatal("写入预设失败")
	}
	if _, err := s.ApplyPreset("旧预设"); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get(AdminIDsKey)
	if !ok {
		t.Fatal("应用旧预设后应写入 bot.admin_ids")
	}
	if want := []any{"qq:333"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bot.admin_ids = %#v, want %#v", got, want)
	}
	if _, ok := s.Get(LegacyAdminIDKey); ok {
		t.Fatal("应用旧预设不应写入旧键 bot.admin_id")
	}
}

// TestApplyPresetPrefersAdminIDsList 快照同时含新旧键时以列表为准。
func TestApplyPresetPrefersAdminIDsList(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	p := Preset{Name: "混合", CreatedAt: now, UpdatedAt: now, Config: map[string]any{
		LegacyAdminIDKey: "qq:333",
		AdminIDsKey:      []any{"qq:444"},
	}}
	if ok := s.presets.Set(context.Background(), "混合", p); !ok {
		t.Fatal("写入预设失败")
	}
	if _, err := s.ApplyPreset("混合"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(AdminIDsKey)
	if want := []any{"qq:444"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bot.admin_ids = %#v, want %#v", got, want)
	}
	if _, ok := s.Get(LegacyAdminIDKey); ok {
		t.Fatal("应用预设不应写入旧键 bot.admin_id")
	}
}
