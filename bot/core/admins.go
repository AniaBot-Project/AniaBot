package core

import (
	"strings"

	"github.com/jeanhua/AniaBot/bot/core/configstore"
	"github.com/jeanhua/AniaBot/common/model/message"
	"github.com/spf13/viper"
)

// adminIDsFromConfig 读取管理员列表配置（bot.admin_ids，可配置多个），并兼容
// 旧版单值 bot.admin_id（迁移未覆盖，或 viper 由外部注入时仍可能只有旧键）。
// 结果去空、去重并按配置顺序规范化平台前缀。
func adminIDsFromConfig(cfg *viper.Viper) []message.QID {
	if cfg == nil {
		return nil
	}
	raw := cfg.GetStringSlice(configstore.AdminIDsKey)
	if len(raw) == 0 {
		if legacy := strings.TrimSpace(cfg.GetString(configstore.LegacyAdminIDKey)); legacy != "" {
			raw = []string{legacy}
		}
	}
	out := make([]message.QID, 0, len(raw))
	seen := make(map[message.QID]struct{}, len(raw))
	for _, item := range raw {
		id := message.FromString(strings.TrimSpace(item))
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
