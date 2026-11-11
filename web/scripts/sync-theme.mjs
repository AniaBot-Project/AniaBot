// 从 animal-island-vue 同步主题样式到 src/styles/animal-island.vendor.css。
//
// 为什么要同步而不是直接 `import 'animal-island-vue/style'`：
// 官方样式入口内联了 3 个 Noto Sans SC 中文子集（各约 1.1MB，共 3.4MB），
// 而面板是嵌进 Go 二进制的内网管理页，中文用系统字体（PingFang / 微软雅黑）
// 观感几乎一致，没必要为此让产物和二进制膨胀三倍。这里只保留 Nunito 拉丁字体。
//
// 依赖版本号变化后需要重新执行（npm run sync:theme，predev/prebuild 已自动挂载）。

import { createRequire } from 'node:module'
import { copyFileSync, mkdirSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const webRoot = resolve(here, '..')
const outDir = resolve(webRoot, 'src/styles')
const outFiles = join(outDir, 'files')

const require = createRequire(import.meta.url)
// 包的 exports 只暴露 "./style"（= dist/index.css），从样式入口反推包目录与版本
const cssPath = require.resolve('animal-island-vue/style')
const distDir = dirname(cssPath)
const pkgDir = dirname(distDir)
const pkg = JSON.parse(readFileSync(join(pkgDir, 'package.json'), 'utf8'))

let css = readFileSync(cssPath, 'utf8')

// 1. 移除中文字体 @font-face（Nunito 拉丁字体保留，圆体字形是主题风格的一部分）
const removedFaces = []
css = css.replace(/@font-face\{[^}]*\}/g, (block) => {
  if (/font-family:\s*Noto Sans SC/i.test(block)) {
    removedFaces.push(block)
    return ''
  }
  return block
})

// 2. 复制仍然被引用的字体 / 图片资源（路径是相对本文件所在目录的 ./files/xxx）
const referenced = new Set()
for (const m of css.matchAll(/url\(\.\/files\/([^)'"]+)\)/g)) referenced.add(m[1])

rmSync(outFiles, { recursive: true, force: true })
mkdirSync(outFiles, { recursive: true })
for (const name of referenced) {
  copyFileSync(join(pkgDir, 'dist/files', name), join(outFiles, name))
}

// 3. 输出（带来源标注，见 MIT 许可）
const banner = `/* 本文件由 web/scripts/sync-theme.mjs 自动生成，请勿手动修改。
 * 来源：animal-island-vue v${pkg.version}（MIT）https://github.com/guokaigdg/animal-island-vue
 * 处理：移除 Noto Sans SC 中文子集 @font-face（${removedFaces.length} 个），中文回退系统字体；其余样式原样保留。
 */
`
mkdirSync(outDir, { recursive: true })
writeFileSync(join(outDir, 'animal-island.vendor.css'), banner + css, 'utf8')

const files = readdirSync(outFiles)
console.log(`[sync-theme] animal-island-vue v${pkg.version} → src/styles/animal-island.vendor.css（移除中文字体 ${removedFaces.length} 个，资源 ${files.length} 个）`)
