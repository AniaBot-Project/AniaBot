package plugininfo

// SkillInfo 是面板展示的 skill 元信息
type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Location    string   `json:"location"` // skill 在 skills 目录下的相对位置（目录名或文件名）
	Refs        []string `json:"refs"`     // 附属 Markdown 文档
	Extras      []string `json:"extras"`   // 其他附带文件（脚本等）
	Builtin     bool     `json:"builtin"`  // 内置技能：不可删除、不可禁用
	Disabled    bool     `json:"disabled"` // 外置技能是否被禁用（禁用后描述不进入上下文）
}

// SkillFileInfo 是 skill 详情中的附属文件信息。
type SkillFileInfo struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // reference / extra
	Size    int64  `json:"size"`
	Content string `json:"content,omitempty"` // 文本类文件返回正文（Markdown 附属文档与可预览的文本附带文件）
}

// SkillDetail 是面板查看 SKILL 详情时返回的完整内容。
type SkillDetail struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Location    string          `json:"location"`
	Content     string          `json:"content"` // SKILL.md 完整内容（含 frontmatter）
	Files       []SkillFileInfo `json:"files"`
	Builtin     bool            `json:"builtin"`
	Disabled    bool            `json:"disabled"`
}
