package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// NamespacePresets 配置预设在持久化存储中的保留命名空间。
// 与配置本体（Namespace）分离，避免预设数据泄漏进 All()/ToViper()。
const NamespacePresets = "__config_presets"

// Preset 配置预设：一份完整的配置快照。
type Preset struct {
	Name      string         `json:"name"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Config    map[string]any `json:"config"`
}

// PresetInfo 预设列表项（不含配置快照本体）。
type PresetInfo struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	KeyCount  int       `json:"key_count"`
}

// presetNameMaxLen 预设名最大长度（字符数）
const presetNameMaxLen = 64

// validatePresetName 校验预设名：去空白后非空、不含控制字符、长度受限。
func validatePresetName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("预设名不能为空")
	}
	if utf8.RuneCountInString(name) > presetNameMaxLen {
		return "", fmt.Errorf("预设名过长（最多 %d 个字符）", presetNameMaxLen)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("预设名包含非法字符")
		}
	}
	return name, nil
}

// SavePreset 将当前全部配置保存为指定名称的预设（同名覆盖，保留创建时间）。
func (s *Store) SavePreset(name string) error {
	name, err := validatePresetName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	p := Preset{
		Name:      name,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Config:    s.allLocked(),
	}
	// 同名覆盖时保留原创建时间
	if old, ok := s.getPresetLocked(name); ok {
		p.CreatedAt = old.CreatedAt
	}
	if ok := s.presets.Set(context.Background(), name, p); !ok {
		return fmt.Errorf("保存预设失败")
	}
	s.logger.Info("配置预设已保存", "name", name, "keys", len(p.Config))
	return nil
}

// PresetList 返回全部预设的概要列表（按名称排序）。
// 同时修复历史脏数据：早期 QQ ID 前缀迁移会把预设的 name/时间戳清成零值
// （存储键不受影响），导致面板出现无法应用/删除的无名预设；此处按存储键
// 回填名称并持久化修复。
func (s *Store) PresetList() []PresetInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, err := s.presets.Keys(context.Background(), "")
	if err != nil {
		s.logger.Warn("列出配置预设失败", "error", err)
		return []PresetInfo{}
	}
	out := make([]PresetInfo, 0, len(keys))
	for _, k := range keys {
		p, ok := s.getPresetLocked(k)
		if !ok {
			continue
		}
		if p.Name == "" {
			p.Name = k
			if p.CreatedAt.IsZero() {
				p.CreatedAt = time.Now()
			}
			if p.UpdatedAt.IsZero() {
				p.UpdatedAt = p.CreatedAt
			}
			if s.presets.Set(context.Background(), k, p) {
				s.logger.Info("已修复丢失名称的配置预设", "name", k)
			} else {
				s.logger.Warn("修复配置预设失败", "name", k)
			}
		}
		out = append(out, PresetInfo{
			Name:      p.Name,
			CreatedAt: p.CreatedAt,
			UpdatedAt: p.UpdatedAt,
			KeyCount:  len(p.Config),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GetPreset 读取指定预设的配置快照。
func (s *Store) GetPreset(name string) (map[string]any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.getPresetLocked(name)
	if !ok {
		return nil, false
	}
	return p.Config, true
}

// ApplyPreset 将预设快照写回配置中心（仅覆盖快照中包含的键，不删除其他键）。
// 与面板配置保存一致：不允许把面板监听地址置空。返回写入的键数量。
func (s *Store) ApplyPreset(name string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.getPresetLocked(name)
	if !ok {
		return 0, fmt.Errorf("预设不存在: %s", name)
	}
	cfg := make(map[string]any, len(p.Config))
	for k, v := range p.Config {
		cfg[k] = v
	}
	// 兼容旧版预设：快照里的单管理员键 bot.admin_id 转换为列表键 bot.admin_ids
	// （快照已含列表键时以列表为准，丢弃旧键）。
	if legacy, ok := cfg[LegacyAdminIDKey]; ok {
		delete(cfg, LegacyAdminIDKey)
		if _, hasList := cfg[AdminIDsKey]; !hasList {
			cfg[AdminIDsKey] = legacyAdminIDs(legacy)
		}
	}
	n := 0
	for k, v := range cfg {
		if k == "bot.admin_panel.listen" {
			if str, isStr := v.(string); !isStr || str == "" {
				continue
			}
		}
		if err := s.setLocked(k, v); err != nil {
			return n, err
		}
		n++
	}
	s.logger.Info("配置预设已应用", "name", name, "keys", n)
	return n, nil
}

// DeletePreset 删除指定预设，返回预设是否存在。
func (s *Store) DeletePreset(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.presets.Has(context.Background(), name) {
		return false
	}
	if ok := s.presets.Del(context.Background(), name); !ok {
		s.logger.Warn("删除配置预设失败", "name", name)
		return false
	}
	s.logger.Info("配置预设已删除", "name", name)
	return true
}

// getPresetLocked 读取预设（调用方需持有锁）。
func (s *Store) getPresetLocked(name string) (Preset, bool) {
	var p Preset
	if !s.presets.Get(context.Background(), name, &p) {
		return Preset{}, false
	}
	if p.Config == nil {
		p.Config = map[string]any{}
	}
	return p, true
}

// allLocked 是 All 的无锁版本（调用方需持有锁）。
func (s *Store) allLocked() map[string]any {
	keys, err := s.store.Keys(context.Background(), "")
	if err != nil {
		s.logger.Warn("列出配置键失败", "error", err)
		return map[string]any{}
	}
	out := make(map[string]any, len(keys))
	for _, k := range keys {
		if strings.HasPrefix(k, "meta.") {
			continue
		}
		raw, ok := s.store.GetString(context.Background(), k)
		if !ok {
			continue
		}
		var val any
		if err := json.Unmarshal([]byte(raw), &val); err != nil {
			continue
		}
		out[k] = val
	}
	return out
}
