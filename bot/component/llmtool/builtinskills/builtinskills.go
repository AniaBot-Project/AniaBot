// Package builtinskills 提供随二进制发布的内置 skill 资源。
//
// 内置 skill 不受 skills 白名单限制、不可删除、不可禁用；SkillManager 加载时
// 会把它物化到 <skillsDir>/.builtin，附带脚本因此拥有可直接执行的绝对路径
// （bash 工具读取 skill_read 输出的「附带文件」列表即可拿到）。
package builtinskills

import "embed"

// FS 是内置 skill 资源集合。
//
//go:embed skills
var FS embed.FS

// Root 是内置 skill 集合在 FS 中的根目录（其下每个子目录为一个 skill）。
const Root = "skills"
