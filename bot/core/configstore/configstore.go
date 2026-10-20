// Package configstore 提供基于持久化存储的配置中心。
//
// AniaBot 的全部配置以点分键（与历史 viper 键一致，如 plugin.ai_chat_bot.base_url）
// 的形式存储在持久化存储的保留命名空间 __config 下，值以 JSON 编码以保留类型
// （string / float / bool / 数组）。
//
// 首次启动时通过 [Store.Init] 写入默认配置（内嵌的 config_tmpl.yaml），
// 之后通过 [Store.ToViper] 构建内存 viper 供框架与插件使用——插件的
// Start(ctx, *viper.Viper) 接口与读取语义（Get*/IsSet/UnmarshalKey）完全不变。
package configstore

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/jeanhua/AniaBot/common/storage"
	"github.com/spf13/viper"
)

// Namespace 配置在持久化存储中的保留命名空间。
const Namespace = "__config"

// 特殊配置键：MCP 服务器 / Prompt 覆盖 / AI 钩子 / 自定义命令的原始 JSON 文本
const (
	KeyMCPJSON      = "files.mcp_json"
	KeyPromptJSON   = "files.prompt_json"
	KeyHooksJSON    = "files.hooks_json"
	KeyCommandsJSON = "files.commands_json"
)

// 内部元数据键（不进入 viper）
const (
	metaInitialized  = "meta.initialized"
	metaSetupPending = "meta.setup_pending"
)

//go:embed config_tmpl.yaml
var defaultConfigYAML []byte

// Store 配置中心。并发安全。
type Store struct {
	store   storage.PersistentStorage
	presets storage.PersistentStorage // 配置预设（独立命名空间，不进入 All()/ToViper()）
	logger  *slog.Logger
	mu      sync.RWMutex
}

// New 基于根持久化存储创建配置中心（内部 Clone 到 __config 命名空间）。
func New(root storage.PersistentStorage, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{store: root.Clone(Namespace), presets: root.Clone(NamespacePresets), logger: logger}
}

// Init 初始化配置：已完成则直接返回；否则写入默认配置并标记待完成设置向导。
func (s *Store) Init() error {
	ctx := context.Background()
	if s.store.Has(ctx, metaInitialized) {
		return nil
	}

	if err := s.seedDefaults(); err != nil {
		return fmt.Errorf("写入默认配置失败: %w", err)
	}
	s.logger.Info("已写入默认配置（可在 Web 控制面板中修改）")
	// 全新安装：标记待完成设置向导
	s.store.SetString(ctx, metaSetupPending, "1")

	if ok := s.store.SetString(ctx, metaInitialized, "1"); !ok {
		return fmt.Errorf("写入配置初始化标记失败")
	}
	return nil
}

// seedDefaults 写入内嵌默认配置（config_tmpl.yaml）。
func (s *Store) seedDefaults() error {
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(string(defaultConfigYAML))); err != nil {
		return err
	}
	flat := map[string]any{}
	flatten(v.AllSettings(), "", flat)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, val := range flat {
		if err := s.setLocked(k, val); err != nil {
			return err
		}
	}
	return nil
}

// flatten 将嵌套的 map 递归拍平为点分键。nil 叶子跳过（如空的 admin_ids）。
func flatten(m map[string]any, prefix string, out map[string]any) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch child := v.(type) {
		case map[string]any:
			flatten(child, key, out)
		case nil:
			// 空值不写入，保持 IsSet=false 语义
		default:
			out[key] = v
		}
	}
}

// SetupPending 返回是否为待完成首次设置向导的全新安装。
func (s *Store) SetupPending() bool {
	v, ok := s.store.GetString(context.Background(), metaSetupPending)
	return ok && v == "1"
}

// CompleteSetup 标记首次设置向导已完成（或已跳过）。
func (s *Store) CompleteSetup() {
	s.store.Del(context.Background(), metaSetupPending)
}

// EnsureDefaults 写入缺失的默认配置（已存在的键不覆盖）。
// 用于插件注册配置字段（pluginconfig）后填充默认值——
// 插件升级新增的配置键也能在下次启动时自动补齐。
func (s *Store) EnsureDefaults(defaults map[string]any) {
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range defaults {
		if v == nil {
			continue
		}
		key := strings.ToLower(k)
		if s.store.Has(ctx, key) {
			continue
		}
		if err := s.setLocked(key, v); err != nil {
			s.logger.Warn("写入默认配置失败", "key", key, "error", err)
		}
	}
}

// Set 写入一个配置键（值 JSON 编码）。键统一转小写存储，与 viper 的
// 大小写不敏感语义保持一致（避免同一逻辑键因大小写不同出现两份）。
func (s *Store) Set(key string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setLocked(strings.ToLower(key), val)
}

func (s *Store) setLocked(key string, val any) error {
	data, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("编码配置值失败 %s: %w", key, err)
	}
	if ok := s.store.SetString(context.Background(), key, string(data)); !ok {
		return fmt.Errorf("写入配置失败: %s", key)
	}
	return nil
}

// SetMany 批量写入配置键。
func (s *Store) SetMany(kvs map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range kvs {
		if err := s.setLocked(strings.ToLower(k), v); err != nil {
			return err
		}
	}
	return nil
}

// Get 读取一个配置键（JSON 解码为原生类型）。
func (s *Store) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	raw, ok := s.store.GetString(context.Background(), strings.ToLower(key))
	if !ok {
		return nil, false
	}
	var val any
	if err := json.Unmarshal([]byte(raw), &val); err != nil {
		return nil, false
	}
	return val, true
}

// Delete 删除一个配置键。
func (s *Store) Delete(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.Del(context.Background(), strings.ToLower(key))
}

// All 返回全部配置键值（不含 meta.* 内部键）。
func (s *Store) All() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.allLocked()
}

// envPrefix 环境变量覆盖前缀：配置键 bot.admin_panel.listen 对应
// 环境变量 ANIA_BOT_ADMIN_PANEL_LISTEN（点与横线转为下划线，全大写）。
const envPrefix = "ANIA_"

var envKeyReplacer = strings.NewReplacer(".", "_", "-", "_")

// ToViper 将配置中心的全部配置构建为内存 viper，
// 供框架与插件按既有方式读取（Get*/IsSet/UnmarshalKey 语义不变）。
// 环境变量（ANIA_ 前缀）优先级高于数据库中的配置值，便于容器部署时覆盖。
func (s *Store) ToViper() *viper.Viper {
	v := viper.New()
	for k, val := range s.All() {
		v.Set(k, val)
	}
	s.applyEnvOverrides(v)
	return v
}

// applyEnvOverrides 用 ANIA_ 前缀的环境变量覆盖对应配置键。
// viper 的显式 Set 优先级高于 AutomaticEnv，故需手动覆盖。
func (s *Store) applyEnvOverrides(v *viper.Viper) {
	for _, k := range v.AllKeys() {
		envKey := envPrefix + strings.ToUpper(envKeyReplacer.Replace(k))
		raw, ok := os.LookupEnv(envKey)
		if !ok {
			continue
		}
		var val any = raw
		// 保持原值类型：非字符串类型按 JSON 解析（如 int/bool/数组）
		if _, isStr := v.Get(k).(string); !isStr {
			var decoded any
			if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
				val = decoded
			}
		}
		v.Set(k, val)
		s.logger.Info("环境变量覆盖配置", "key", k, "env", envKey)
	}
}
