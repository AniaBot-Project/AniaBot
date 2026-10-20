package configstore

import (
	"context"
	"encoding/json"
	"strings"
)

// 管理员配置键。
const (
	// AdminIDsKey 管理员 ID 列表（可配置多个）。
	AdminIDsKey = "bot.admin_ids"
	// LegacyAdminIDKey 旧版单管理员 ID 键（迁移后删除）。
	LegacyAdminIDKey = "bot.admin_id"
)

// MigrateAdminIDs 把旧版单管理员配置（bot.admin_id）迁移为管理员列表
// （bot.admin_ids）：旧值追加到列表末尾（已存在则跳过），随后删除旧键。
// 旧键不存在时什么都不做；迁移幂等，可每次启动执行。
func (s *Store) MigrateAdminIDs() error {
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()

	rawLegacy, ok := s.store.GetString(ctx, LegacyAdminIDKey)
	if !ok {
		return nil
	}
	var legacy string
	if err := json.Unmarshal([]byte(rawLegacy), &legacy); err != nil {
		// 旧值不是字符串（异常数据）：删掉旧键即可，避免每次启动重复报错
		s.store.Del(ctx, LegacyAdminIDKey)
		return nil
	}
	legacy = strings.TrimSpace(legacy)

	ids, err := s.adminIDsLocked(ctx)
	if err != nil {
		// 列表值无法解析（异常数据）：用旧版单值重建，避免管理员配置一直失效
		s.logger.Warn("管理员列表配置无法解析，将用旧版单管理员配置重建", "error", err)
		ids = nil
	}
	changed := false
	if legacy != "" && !containsString(ids, legacy) {
		ids = append(ids, legacy)
		changed = true
	}
	if changed {
		if err := s.setLocked(AdminIDsKey, ids); err != nil {
			return err
		}
	}
	if ok := s.store.Del(ctx, LegacyAdminIDKey); !ok {
		s.logger.Warn("删除旧版管理员配置失败", "key", LegacyAdminIDKey)
	}
	if changed {
		s.logger.Info("管理员配置已迁移为列表形式", "count", len(ids))
	}
	return nil
}

// adminIDsLocked 读取管理员列表（调用方需持有锁）。
func (s *Store) adminIDsLocked(ctx context.Context) ([]string, error) {
	raw, ok := s.store.GetString(ctx, AdminIDsKey)
	if !ok {
		return nil, nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// legacyAdminIDs 把旧版单值管理员配置转换为列表值（非字符串/空值 → 空列表）。
func legacyAdminIDs(v any) []string {
	str, ok := v.(string)
	if !ok {
		return []string{}
	}
	str = strings.TrimSpace(str)
	if str == "" {
		return []string{}
	}
	return []string{str}
}

// containsString 判断字符串切片是否包含目标值。
func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
