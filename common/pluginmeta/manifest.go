// Package pluginmeta 定义插件市场插件的元信息格式（plugin.json / index.json）与校验逻辑。
//
// 该包是叶子包，供安装流水线（bot/marketplace）、注册代码生成器（tools/plugingen）
// 与面板共用，保证市场元信息的单一事实来源。
package pluginmeta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// 当前插件 API 版本：框架公共接口（common/plugin 等）发生不兼容变更时递增，
// 旧插件会在安装/列表时被标记为不兼容。
const APIVersion = 1

// 默认构造函数名。
const DefaultConstructor = "NewPlugin"

// 默认 README 文件名。
const DefaultReadme = "README.md"

// ModulePath AniaBot 主模块路径。
const ModulePath = "github.com/jeanhua/AniaBot"

// PluginRoot 市场插件在 AniaBot 源码树中的安装目录（相对仓库根）。
const PluginRoot = "custom/plugins"

var idRe = regexp.MustCompile(`^[a-z0-9_-]{2,64}$`)

// Entry 插件入口声明。
type Entry struct {
	// Constructor 插件构造函数名，默认 NewPlugin；必须返回 common/plugin.Plugin。
	Constructor string `json:"constructor,omitempty"`
}

// Manifest 单个插件元信息（plugin.json 与 index.json 条目共用同一结构）。
type Manifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Author       string   `json:"author"`
	Version      string   `json:"version"`
	Platforms    []string `json:"platforms,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	APIVersion   int      `json:"api_version,omitempty"`
	MinFramework string   `json:"min_framework,omitempty"`
	Entry        Entry    `json:"entry,omitempty"`
	Readme       string   `json:"readme,omitempty"`
	Icon         string   `json:"icon,omitempty"`
}

// Index 插件仓库聚合索引（index.json）。
type Index struct {
	Plugins []Manifest `json:"plugins"`
}

// Constructor 返回插件构造函数名（缺省 DefaultConstructor）。
func (m *Manifest) Constructor() string {
	if m.Entry.Constructor != "" {
		return m.Entry.Constructor
	}
	return DefaultConstructor
}

// ReadmeName 返回 README 文件名（缺省 DefaultReadme）。
func (m *Manifest) ReadmeName() string {
	if m.Readme != "" {
		return m.Readme
	}
	return DefaultReadme
}

// ImportPath 返回插件包在 AniaBot 主模块中的 import 路径。
func ImportPath(id string) string {
	return ModulePath + "/" + PluginRoot + "/" + id
}

// ValidateID 校验插件 ID 合法性（同时用于元信息校验与面板批量操作等外部输入防护）。
func ValidateID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("插件 ID 非法（须为 2~64 位小写字母/数字/-/_）: %q", id)
	}
	return nil
}

// Validate 校验元信息必填字段与 ID 合法性；缺失的可选字段就地补默认值。
func (m *Manifest) Validate() error {
	if m == nil {
		return fmt.Errorf("插件元信息为空")
	}
	if err := ValidateID(m.ID); err != nil {
		return err
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("插件 %s: name 必填", m.ID)
	}
	if strings.TrimSpace(m.Description) == "" {
		return fmt.Errorf("插件 %s: description 必填", m.ID)
	}
	if strings.TrimSpace(m.Author) == "" {
		return fmt.Errorf("插件 %s: author 必填", m.ID)
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("插件 %s: version 必填", m.ID)
	}
	if m.APIVersion == 0 {
		m.APIVersion = APIVersion
	}
	if m.APIVersion != APIVersion {
		return fmt.Errorf("插件 %s: api_version=%d 与当前框架 %d 不兼容", m.ID, m.APIVersion, APIVersion)
	}
	if m.Readme == "" {
		m.Readme = DefaultReadme
	}
	// 图标只允许仓库内的普通文件名，禁止路径穿越
	if m.Icon != "" && (filepath.Base(m.Icon) != m.Icon || strings.ContainsAny(m.Icon, `/\`)) {
		return fmt.Errorf("插件 %s: icon 必须是文件名（不能含路径）: %q", m.ID, m.Icon)
	}
	return nil
}

// LoadManifest 从 JSON 文件加载并校验插件元信息。
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析插件元信息失败 %s: %w", path, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
