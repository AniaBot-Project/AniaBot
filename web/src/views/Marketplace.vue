<template>
  <div class="space-y-5 max-w-6xl">
    <!-- 环境提示 -->
    <div v-if="info && info.mode === 'dev'" class="tcard p-4 border-l-2 border-l-amber-400 flex items-start gap-3">
      <span class="tdot bg-[#ff9f0a] mt-1.5 shrink-0" />
      <div class="text-xs text-zinc-600 leading-relaxed">
        当前为 <span class="font-semibold text-zinc-900">go run 开发模式</span>运行，插件市场不可用。
        请以编译后的二进制方式部署（容器内可直接使用）。
      </div>
    </div>
    <div v-else-if="info && !info.enabled" class="tcard p-4 border-l-2 border-l-amber-400 flex items-start gap-3">
      <span class="tdot bg-[#ff9f0a] mt-1.5 shrink-0" />
      <div class="text-xs text-zinc-600 leading-relaxed">
        插件市场未开启。请先在
        <RouterLink to="/config" class="font-semibold text-zinc-900 underline underline-offset-2">配置管理</RouterLink>
        的「插件市场」分组中设置 <span class="font-mono">bot.marketplace.enable=true</span>。
      </div>
    </div>
    <div v-else-if="info && !info.configured" class="tcard p-4 border-l-2 border-l-amber-400 flex items-start gap-3">
      <span class="tdot bg-[#ff9f0a] mt-1.5 shrink-0" />
      <div class="text-xs text-zinc-600 leading-relaxed">
        尚未配置源码目录。请先在
        <RouterLink to="/config" class="font-semibold text-zinc-900 underline underline-offset-2">配置管理</RouterLink>
        中设置 <span class="font-mono">bot.marketplace.source_dir</span>（或自动更新的
        <span class="font-mono">bot.update.source_dir</span>），并确保已完成一次自动更新。
      </div>
    </div>

    <div v-else-if="!info && !listError" class="tcard p-8 flex items-center justify-center gap-2.5 text-xs text-zinc-400">
      <span class="w-4 h-4 border-2 border-zinc-300 border-t-zinc-600 rounded-full animate-spin" />
      正在加载插件市场...
    </div>

    <!-- 错误提示 -->
    <div v-if="listError" class="tcard p-4 border-l-2 border-l-red-400">
      <p class="text-xs text-[#ff3b30] font-mono break-all leading-relaxed">{{ listError }}</p>
      <p class="text-[11px] text-zinc-400 mt-2">可能是网络不通或触发 GitHub API 限流，可稍后重试或登录 GitHub 后再试。</p>
    </div>

    <template v-if="info && areaReady">
      <!-- 顶部操作栏 -->
      <div class="flex items-center justify-between gap-3 flex-wrap">
        <p class="text-xs text-zinc-500 leading-relaxed">
          <span v-if="plugins.length">共 {{ plugins.length }} 个插件</span>
          <span v-else>插件列表</span>
          <span v-if="syncedAt" class="text-zinc-400">
            · 索引同步于 {{ relTime(syncedAt) }}（{{ fmtTime(syncedAt) }}）
          </span>
          <span v-else class="text-zinc-400">· 尚未同步索引，点击右上角「刷新列表」获取插件</span>
        </p>
        <div class="flex items-center gap-2">
          <button
            v-if="showLogin"
            class="inline-flex items-center gap-2 text-xs btn-accent px-3.5 py-2 rounded-lg font-medium shadow-sm transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="busy || loading || refreshing"
            @click="onOAuthStart"
          >
            <svg class="w-4 h-4" viewBox="0 0 24 24" fill="currentColor"><path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.11.79-.25.79-.55 0-.27-.01-1.17-.02-2.12-3.2.7-3.88-1.36-3.88-1.36-.52-1.33-1.28-1.68-1.28-1.68-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.75 2.69 1.25 3.34.95.1-.74.4-1.25.72-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.28 1.18-3.09-.12-.29-.51-1.46.11-3.05 0 0 .96-.31 3.16 1.18a11 11 0 0 1 5.76 0c2.2-1.49 3.16-1.18 3.16-1.18.62 1.59.23 2.76.11 3.05.73.81 1.18 1.83 1.18 3.09 0 4.42-2.7 5.39-5.27 5.67.41.36.78 1.06.78 2.14 0 1.54-.01 2.78-.01 3.16 0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z"/></svg>
            {{ info.token_set ? '重新登录 GitHub' : '登录 GitHub' }}
          </button>
          <button
            class="inline-flex items-center gap-2 text-xs bg-white text-zinc-700 px-3.5 py-2 rounded-lg border border-zinc-300 hover:bg-white/55 hover:text-zinc-900 font-medium shadow-sm transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="!canBrowse || loading || refreshing"
            @click="refreshList()"
          >
            <svg v-if="refreshing" class="w-3.5 h-3.5 animate-spin" fill="none" viewBox="0 0 24 24" stroke-width="2" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99" /></svg>
            <svg v-else class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992v-.001M2.985 19.644v-4.992m0 0h4.992m-4.993 0 3.181 3.183a8.25 8.25 0 0 0 13.803-3.7M4.031 9.865a8.25 8.25 0 0 1 13.803-3.7l3.181 3.182m0-4.991v4.99" /></svg>
            {{ refreshing ? '同步中...' : '刷新列表' }}
          </button>
        </div>
      </div>

      <!-- 市场信息 -->
      <section class="tcard p-5">
        <div class="flex items-center justify-between gap-3 flex-wrap mb-5">
          <span class="tlabel">Marketplace / 仓库信息</span>
          <div class="flex items-center gap-2 flex-wrap">
            <span v-if="info.token_valid" class="tpill"><span class="tdot bg-[#34c759]" />已登录 GitHub</span>
            <span v-else-if="info.token_set" class="tpill"><span class="tdot bg-red-400" />登录已失效</span>
            <span v-else class="tpill"><span class="tdot bg-zinc-300" />未登录</span>
            <span v-if="info.rate_remaining >= 0" class="tpill"><span class="tdot bg-zinc-300" />配额 {{ info.rate_remaining }}</span>
          </div>
        </div>

        <dl class="grid grid-cols-2 md:grid-cols-3 xl:grid-cols-5 gap-x-4 gap-y-5">
          <div class="min-w-0">
            <dt class="tlabel mb-1.5">插件仓库</dt>
            <dd class="font-mono text-sm text-zinc-900 truncate" :title="info.repo">{{ info.repo }}</dd>
          </div>
          <div class="min-w-0">
            <dt class="tlabel mb-1.5">分支</dt>
            <dd class="font-mono text-sm text-zinc-900 truncate">{{ info.branch }}</dd>
          </div>
          <div>
            <dt class="tlabel mb-1.5">已安装</dt>
            <dd class="text-sm text-zinc-900">{{ info.installed ?? '—' }} 个插件</dd>
          </div>
          <div class="min-w-0">
            <dt class="tlabel mb-1.5">运行环境</dt>
            <dd class="flex flex-col gap-1.5">
              <span v-for="t in ['git', 'go']" :key="t" class="flex items-center gap-2 text-xs min-w-0">
                <span class="tdot" :class="info.env?.[t] ? 'bg-[#34c759]' : 'bg-red-400'" />
                <span class="uppercase tracking-wider text-zinc-400 w-8 shrink-0">{{ t }}</span>
                <span class="font-mono text-zinc-700 truncate" :title="info.env?.[t]">{{ info.env?.[t] || '未安装' }}</span>
              </span>
            </dd>
          </div>
          <div>
            <dt class="tlabel mb-1.5">索引同步</dt>
            <dd>
              <template v-if="syncedAt">
                <div class="text-sm text-zinc-900">{{ relTime(syncedAt) }}</div>
                <div class="text-[10px] text-zinc-400 mt-0.5 font-mono">{{ fmtTime(syncedAt) }}</div>
              </template>
              <div v-else class="text-sm text-zinc-400">尚未同步</div>
            </dd>
          </div>
        </dl>

        <!-- GitHub 账号状态 -->
        <div class="mt-5 pt-4 border-t border-zinc-100 flex items-center justify-between gap-3 flex-wrap">
          <div class="flex items-center gap-2 flex-wrap text-xs">
            <span class="tdot" :class="accountDotClass" />
            <span class="font-medium" :class="accountTextClass">{{ accountText }}</span>
            <span v-if="info.rate_remaining >= 0" class="text-[11px] text-zinc-400">剩余 API 配额 {{ info.rate_remaining }}</span>
          </div>
          <p v-if="!info.oauth_configured && !info.token_valid" class="text-[11px] text-zinc-400 leading-relaxed">
            在线登录需先在「配置管理 → 插件市场」设置 GitHub OAuth App 的 Client ID（启用 Device flow）后重启；
            也可直接填写 <code class="font-mono">bot.marketplace.token</code>。
          </p>
          <p v-else-if="info.token_set && !info.token_valid" class="text-[11px] text-red-400 leading-relaxed">
            登录已失效，点击右上角「重新登录 GitHub」重新授权即可恢复高配额。
          </p>
        </div>

        <!-- 回滚（仅存在备份时展示） -->
        <div v-if="info.rollback_available" class="mt-4 pt-4 border-t border-zinc-100 flex items-center justify-between gap-3 flex-wrap">
          <p class="text-xs text-zinc-500 leading-relaxed">上次安装保留了旧版本备份；如安装后出现异常，可回滚到操作前的状态（将重启 Bot）。</p>
          <button
            class="inline-flex items-center gap-1.5 text-xs text-[#ff3b30] bg-red-50 border border-red-200 hover:bg-red-100 px-3 py-1.5 rounded-lg font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            :disabled="busy"
            @click="onRollback"
          >
            <svg class="w-3.5 h-3.5" fill="none" viewBox="0 0 24 24" stroke-width="1.8" stroke="currentColor"><path stroke-linecap="round" stroke-linejoin="round" d="M9 15 3 9m0 0 6-6M3 9h12a6 6 0 0 1 0 12h-3" /></svg>
            回滚上次安装
          </button>
        </div>
      </section>

      <!-- 任务进度 -->
      <section v-if="started" ref="progressEl" class="tcard p-5">
        <div class="flex items-center justify-between gap-3 mb-4">
          <span class="tlabel">Progress / 任务进度</span>
          <span v-if="status.error" class="tpill"><span class="tdot bg-red-400" />{{ status.errKind || '操作失败' }}</span>
        </div>
        <div v-if="status.restarting" class="mb-3 text-xs text-zinc-500">操作完成，等待 Bot 重启...</div>
        <div class="flex flex-wrap items-center gap-y-2 mb-5">
          <template v-for="(p, i) in phases" :key="p.key">
            <div class="flex items-center gap-1.5">
              <span class="w-5 h-5 rounded-full border flex items-center justify-center text-[10px] font-mono" :class="phaseClass(p.key)">{{ phaseDone(p.key) ? '✓' : i + 1 }}</span>
              <span class="text-[11px]" :class="phaseTextClass(p.key)">{{ p.label }}</span>
            </div>
            <span v-if="i < phases.length - 1" class="mx-2 h-px w-5 bg-zinc-200" />
          </template>
        </div>
        <div ref="logEl" class="bg-slate-950/85 rounded-xl p-3.5 h-64 overflow-y-auto font-mono text-[11px] leading-relaxed text-slate-300 border border-white/10 shadow-inner">
          <div v-for="(l, i) in status.logs" :key="i" :class="logLineClass(l)">{{ l }}</div>
          <div v-if="status.running" class="flex items-center gap-2 text-zinc-500 mt-1">
            <span class="w-3 h-3 border-2 border-zinc-700 border-t-zinc-300 rounded-full animate-spin" />
            执行中...
          </div>
        </div>
      </section>

      <!-- 插件列表 -->
      <section class="tcard overflow-hidden">
        <div class="px-5 pt-5 pb-4 border-b border-zinc-100">
          <div class="flex items-center justify-between gap-3 flex-wrap">
            <div class="flex items-center gap-1 bg-zinc-100 rounded-lg p-1">
              <button
                v-for="t in tabs" :key="t.key"
                class="px-3.5 py-1.5 text-xs rounded-md transition-colors"
                :class="tab === t.key ? 'btn-accent font-medium shadow-sm' : 'text-zinc-500 hover:text-zinc-800'"
                @click="tab = t.key"
              >{{ t.label }}</button>
            </div>
            <input v-model="keyword" placeholder="搜索名称 / 描述 / 作者 / 标签" class="w-full sm:w-72 text-xs bg-white border border-zinc-300 rounded-lg px-3 py-2 focus:outline-none focus:ring-2 focus:ring-[#0071e3]/30 focus:border-[#0071e3]/50 transition-shadow" />
          </div>
          <p class="mt-3 text-[11px] text-zinc-400">显示 {{ filtered.length }} / {{ plugins.length }} 个插件</p>
        </div>

        <div v-if="filtered.length === 0" class="py-14 text-center text-xs text-zinc-400">
          <template v-if="loading">正在加载插件...</template>
          <template v-else-if="keyword">没有匹配「{{ keyword }}」的插件</template>
          <template v-else-if="tab === 'installed'">还没有安装任何插件，点击插件卡片「安装」试试</template>
          <template v-else-if="tab === 'updatable'">所有插件都已是最新版本</template>
          <template v-else>暂无插件</template>
        </div>
        <div v-else class="p-4 grid grid-cols-1 md:grid-cols-2 gap-3">
          <div
            v-for="p in filtered" :key="p.id"
            class="border border-white/60 rounded-xl p-4 hover:border-zinc-300 hover:shadow-md hover:shadow-zinc-200/50 transition-all cursor-pointer bg-white"
            @click="openDetail(p.id)"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="min-w-0">
                <div class="flex items-center gap-2 flex-wrap">
                  <span class="text-sm font-semibold text-zinc-900 truncate">{{ p.name }}</span>
                  <span v-if="p.installed" class="text-[10px] px-2 py-0.5 rounded-full bg-[#34c759]/10 text-[#248a3d] border border-[#34c759]/40 font-medium">已安装 {{ p.installed_version }}</span>
                  <span v-else-if="p.update_available" class="text-[10px] px-2 py-0.5 rounded-full bg-amber-50 text-amber-700 border border-amber-200 font-medium">可更新</span>
                </div>
                <p class="text-xs text-zinc-500 mt-1.5 line-clamp-2 leading-relaxed">{{ p.description }}</p>
              </div>
            </div>
            <div class="flex items-center justify-between gap-3 mt-3.5">
              <div class="min-w-0 flex items-center gap-2 text-[11px] text-zinc-400 flex-wrap">
                <span class="font-medium text-zinc-600 truncate">{{ p.author }}</span>
                <span class="font-mono shrink-0">v{{ p.version }}</span>
                <span v-if="p.tags?.length" class="flex gap-1 min-w-0">
                  <span v-for="t in p.tags.slice(0, 3)" :key="t" class="px-1.5 py-0.5 rounded bg-zinc-100 text-zinc-500 whitespace-nowrap">{{ t }}</span>
                </span>
              </div>
              <div class="flex items-center gap-1.5 shrink-0" @click.stop>
                <button
                  v-if="p.installed && !p.update_available"
                  class="text-xs text-zinc-500 hover:text-[#ff3b30] hover:bg-red-50 px-2.5 py-1.5 rounded-lg font-medium transition-colors disabled:opacity-50"
                  :disabled="busy"
                  @click="onUninstall(p)"
                >卸载</button>
                <button
                  v-if="!p.installed || p.update_available"
                  class="text-xs px-3 py-1.5 rounded-lg font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  :class="p.installed
                    ? 'bg-amber-50 text-amber-700 border border-amber-200 hover:bg-amber-100'
                  : 'btn-accent '"
                  :disabled="!canOperate || busy"
                  @click="onInstall(p)"
                >{{ p.installed ? '升级' : '安装' }}</button>
              </div>
            </div>
          </div>
        </div>
      </section>
    </template>

    <!-- GitHub 在线登录弹窗 -->
    <div v-if="oauthOpen" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4" v-backdrop-close="() => (oauthOpen = false)">
      <div class="tcard p-6 w-[26rem] max-w-full text-center space-y-4">
        <h2 class="text-sm font-semibold text-zinc-900">GitHub 登录</h2>
        <template v-if="oauth.status === 'pending'">
          <p class="text-xs text-zinc-500 leading-relaxed">请在浏览器打开下面的链接，输入授权码完成登录（15 分钟内有效）</p>
          <a :href="oauth.verification_uri" target="_blank" rel="noopener noreferrer" class="text-sm font-semibold text-zinc-900 underline underline-offset-2 break-all">{{ oauth.verification_uri }}</a>
          <div class="flex items-center justify-center gap-3">
            <span class="text-2xl font-mono font-bold tracking-[0.3em] text-zinc-900 bg-zinc-100 rounded-lg px-4 py-2">{{ oauth.user_code }}</span>
            <button class="text-xs text-zinc-500 hover:text-zinc-900" @click="copyOAuthCode">复制</button>
          </div>
          <p class="text-[11px] text-zinc-400 flex items-center justify-center gap-2">
            <span class="w-3 h-3 border-2 border-zinc-300 border-t-zinc-600 rounded-full animate-spin" />
            等待授权中，请勿关闭本窗口...
          </p>
        </template>
        <template v-else-if="oauth.status === 'authorized'">
          <p class="text-sm text-[#34c759] font-semibold">登录成功{{ oauth.user ? '：' + oauth.user : '' }}</p>
        </template>
        <template v-else>
          <p class="text-sm text-[#ff3b30]">{{ oauth.error || '登录流程已结束' }}</p>
        </template>
        <div class="flex justify-center gap-2 pt-1">
          <button v-if="oauth.status === 'pending'" class="text-xs text-zinc-500 hover:text-zinc-900 px-4 py-2 rounded-lg hover:bg-white/60" @click="onOAuthCancel">取消</button>
        <button v-else class="text-xs btn-accent px-5 py-2 rounded-lg" @click="oauthOpen = false">关闭</button>
        </div>
      </div>
    </div>

    <!-- 详情弹窗 -->
    <div v-if="showDetail" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4 sm:p-6" v-backdrop-close="closeDetail">
      <div class="bg-white rounded-xl shadow-2xl border border-zinc-200 w-full max-w-3xl max-h-[88vh] flex flex-col overflow-hidden">
        <!-- 头部 -->
        <div class="px-6 py-4 border-b border-zinc-100 flex items-start justify-between gap-4 shrink-0">
          <div class="min-w-0 flex-1">
            <template v-if="detailLoading">
              <div class="h-5 w-44 bg-zinc-100 rounded animate-pulse" />
              <div class="h-3.5 w-72 max-w-full bg-zinc-100 rounded mt-2.5 animate-pulse" />
            </template>
            <template v-else-if="detail">
              <div class="flex items-center gap-2 flex-wrap">
                <h2 class="text-lg font-semibold text-zinc-900 truncate">{{ detail.manifest.name }}</h2>
                <span v-if="detail.installed" class="text-[10px] px-2 py-0.5 rounded-full bg-[#34c759]/10 text-[#248a3d] border border-[#34c759]/40 font-medium">已安装 {{ detail.installed_version }}</span>
              </div>
              <p class="text-xs text-zinc-500 mt-1 leading-relaxed">{{ detail.manifest.description }}</p>
            </template>
          </div>
          <button class="text-zinc-400 hover:text-zinc-800 text-xl leading-none shrink-0" @click="closeDetail">✕</button>
        </div>

        <!-- 元信息 + 操作 -->
        <div v-if="detail && !detailLoading" class="px-6 py-3 border-b border-zinc-100 flex items-center justify-between gap-3 flex-wrap shrink-0">
          <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[11px] text-zinc-500">
            <span>作者：<span class="font-medium text-zinc-700">{{ detail.manifest.author }}</span></span>
            <span>版本：<span class="font-mono">v{{ detail.manifest.version }}</span></span>
            <span v-if="detail.manifest.platforms?.length">平台：{{ detail.manifest.platforms.join(' / ') }}</span>
            <span v-if="detail.manifest.min_framework">最低框架：{{ detail.manifest.min_framework }}</span>
          </div>
          <div class="flex items-center gap-2">
            <button
              v-if="!detail.installed || detail.installed_version !== detail.manifest.version"
              class="text-xs px-4 py-1.5 rounded-lg font-medium transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
              :class="detail.installed ? 'bg-amber-50 text-amber-700 border border-amber-200 hover:bg-amber-100' : 'btn-accent '"
              :disabled="!canOperate || busy"
              @click="onInstallDetail"
            >{{ detail.installed ? '升级到 v' + detail.manifest.version : '安装' }}</button>
            <button
              v-if="detail.installed"
              class="text-xs text-zinc-500 hover:text-[#ff3b30] hover:bg-red-50 px-3 py-1.5 rounded-lg font-medium transition-colors disabled:opacity-50"
              :disabled="busy"
              @click="onUninstallDetail"
            >卸载</button>
          </div>
        </div>

        <!-- README -->
        <div class="px-6 py-4 flex-1 min-h-0 overflow-y-auto bg-white">
          <p v-if="detailFromCache && detail && !detailLoading" class="text-[10px] text-zinc-400 mb-2">已展示短期缓存内容，10 分钟内的再次查看不会重复请求</p>
          <template v-if="detailLoading">
            <div class="space-y-2.5">
              <div class="h-3.5 w-3/4 bg-zinc-100 rounded animate-pulse" />
              <div class="h-3.5 w-full bg-zinc-100 rounded animate-pulse" />
              <div class="h-3.5 w-5/6 bg-zinc-100 rounded animate-pulse" />
              <div class="h-3.5 w-2/3 bg-zinc-100 rounded animate-pulse" />
            </div>
          </template>
          <template v-else-if="detail && detail.readme_error">
            <p class="text-xs text-[#ff3b30]">README 加载失败：{{ detail.readme_error }}</p>
          </template>
          <template v-else-if="detail && !detail.readme">
            <p class="text-xs text-zinc-400 py-6 text-center">该插件未提供 README。</p>
          </template>
          <div v-else-if="detail" class="markdown-body" v-html="renderedReadme" />
        </div>
      </div>
    </div>

    <!-- 重启中遮罩 -->
    <div v-if="rebooting" class="fixed inset-0 bg-black/30 backdrop-blur-md flex items-center justify-center z-50 p-4">
      <div class="tcard p-8 w-80 max-w-full text-center space-y-3">
        <span class="mx-auto block w-8 h-8 border-[3px] border-zinc-200 border-t-zinc-800 rounded-full animate-spin" />
        <div class="text-sm font-semibold text-zinc-900 tracking-[0.15em] uppercase">Rebooting</div>
        <p class="text-xs text-zinc-500">插件变更已应用，Bot 正在重启，恢复后页面自动刷新</p>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, reactive, ref } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import { api } from '../api.js'

const phases = [
  { key: 'env', label: '环境检查' },
  { key: 'fetch', label: '下载源码' },
  { key: 'verify', label: '校验' },
  { key: 'copy', label: '写入目录' },
  { key: 'generate', label: '生成注册' },
  { key: 'deps', label: '拉取依赖' },
  { key: 'build', label: '编译' },
  { key: 'swap', label: '替换二进制' },
]
const phaseOrder = phases.map((p) => p.key)

const AUTO_REFRESH_MS = 10 * 60 * 1000 // 索引缓存超过 10 分钟时进入页面自动检查一次
const DETAIL_CACHE_TTL = 10 * 60 * 1000 // 与后端 README 缓存一致

const info = ref(null)
const loading = ref(false)
const refreshing = ref(false)
const listError = ref('')
const plugins = ref([])
const keyword = ref('')
const tab = ref('all')
const tabs = [
  { key: 'all', label: '全部' },
  { key: 'installed', label: '已安装' },
  { key: 'updatable', label: '可更新' },
]
const syncedAt = ref(0)

const showDetail = ref(false)
const detail = ref(null)
const detailLoading = ref(false)
const detailFromCache = ref(false)
const detailCache = new Map()

const started = ref(false)
const rebooting = ref(false)
const progressEl = ref(null)
const logEl = ref(null)
const status = reactive({ running: false, restarting: false, action: '', plugin_id: '', phase: '', logs: [], error: '', errKind: '' })
const oauthOpen = ref(false)
const oauth = reactive({ status: '', user_code: '', verification_uri: '', expires_at: '', error: '', user: '' })
let oauthTimer = null

const areaReady = computed(() => !!info.value && info.value.enabled && info.value.mode === 'binary' && info.value.configured)
const busy = computed(() => status.running || status.restarting || rebooting.value)
const canBrowse = computed(() => areaReady.value && !busy.value)
const canOperate = computed(() => areaReady.value && !busy.value)
const showLogin = computed(() => info.value?.oauth_configured && !info.value?.token_valid && areaReady.value)

const accountDotClass = computed(() => {
  if (info.value?.token_valid) return 'bg-[#34c759]'
  if (info.value?.token_set) return 'bg-red-400'
  return 'bg-zinc-300'
})
const accountTextClass = computed(() => {
  if (info.value?.token_valid) return 'text-[#34c759]'
  if (info.value?.token_set) return 'text-[#ff3b30]'
  return 'text-zinc-500'
})
const accountText = computed(() => {
  const i = info.value
  if (!i) return ''
  if (i.token_valid) return i.oauth_user ? `已登录 GitHub（${i.oauth_user}）` : '已登录 GitHub'
  if (i.token_set) return 'GitHub 登录已失效，请重新登录'
  return '未登录，匿名访问受限（60 次/小时）'
})

const filtered = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return plugins.value.filter((p) => {
    if (tab.value === 'installed' && !p.installed) return false
    if (tab.value === 'updatable' && !p.update_available) return false
    if (!kw) return true
    return [p.name, p.description, p.author, (p.tags || []).join(' ')].join(' ').toLowerCase().includes(kw)
  })
})

const renderedReadme = computed(() => {
  if (!detail.value?.readme) return ''
  return DOMPurify.sanitize(marked.parse(detail.value.readme, { gfm: true, breaks: true }))
})

function fmtTime(unix) {
  if (!unix) return ''
  const d = new Date(unix * 1000)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function relTime(unix) {
  if (!unix) return ''
  const diff = Math.max(0, Date.now() - unix * 1000)
  if (diff < 60 * 1000) return '刚刚'
  if (diff < 60 * 60 * 1000) return `${Math.floor(diff / 60000)} 分钟前`
  if (diff < 24 * 60 * 60 * 1000) return `${Math.floor(diff / 3600000)} 小时前`
  return `${Math.floor(diff / 86400000)} 天前`
}

let timer = null
let lastAutoCheck = 0
onMounted(async () => {
  await load()
  pollStatus()
  timer = setInterval(() => {
    if (!document.hidden) {
      pollStatus()
      maybePeriodicRefresh()
    }
  }, 1500)
  maybePeriodicRefresh()
})
onUnmounted(() => clearInterval(timer))
onUnmounted(() => stopOAuthPoll())

async function load() {
  loading.value = true
  listError.value = ''
  try {
    await loadInfo()
    if (areaReady.value) {
      const data = await api.getMarketplacePlugins(false)
      plugins.value = data.plugins || []
      if (!syncedAt.value) syncedAt.value = Math.floor(Date.now() / 1000) // 首次拉取已写缓存
    }
  } catch (e) {
    listError.value = e.message
  } finally {
    loading.value = false
  }
}

async function loadInfo() {
  try {
    info.value = await api.getMarketplaceInfo()
    syncedAt.value = info.value?.index_synced_at || 0
  } catch (e) {
    listError.value = e.message
  }
}

async function refreshList(manual = true) {
  if (!areaReady.value || loading.value || refreshing.value || busy.value) return
  refreshing.value = true
  listError.value = ''
  try {
    const data = await api.getMarketplacePlugins(true)
    plugins.value = data.plugins || []
    syncedAt.value = Math.floor(Date.now() / 1000)
    detailCache.clear() // 索引已更新，旧详情缓存作废
    if (manual) await loadInfo() // 手动刷新时同步账号/配额/回滚状态
  } catch (e) {
    listError.value = e.message
  } finally {
    refreshing.value = false
  }
}

// 缓存索引较旧时（进入页面或停留中每隔一段时间）自动检查一次，
// 避免不点刷新就一直看不到新插件；手动刷新按钮仍然保留
function maybePeriodicRefresh() {
  if (Date.now() - lastAutoCheck < AUTO_REFRESH_MS) return
  if (!areaReady.value || loading.value || refreshing.value || busy.value) return
  if (syncedAt.value > 0 && Date.now() - syncedAt.value * 1000 > AUTO_REFRESH_MS) {
    lastAutoCheck = Date.now()
    refreshList(false)
  }
}

function closeDetail() {
  showDetail.value = false
  detail.value = null
  detailLoading.value = false
  detailFromCache.value = false
}

async function openDetail(id) {
  if (showDetail.value && detailLoading.value) return
  showDetail.value = true
  detail.value = null
  detailLoading.value = true
  detailFromCache.value = false
  // 前端短期缓存：同一会话内反复点开直接展示，不再重复请求
  const cached = detailCache.get(id)
  if (cached && Date.now() - cached.ts < DETAIL_CACHE_TTL) {
    detail.value = cached.data
    detailFromCache.value = true
    detailLoading.value = false
    return
  }
  try {
    const data = await api.getMarketplaceDetail(id)
    detailCache.set(id, { data, ts: Date.now() })
    detail.value = data
  } catch (e) {
    listError.value = e.message
    closeDetail()
  } finally {
    detailLoading.value = false
  }
}

function detailAction() {
  if (!detail.value) return null
  return {
    id: detail.value.manifest.id,
    name: detail.value.manifest.name,
    installed: detail.value.installed,
  }
}

async function onInstallDetail() {
  const p = detailAction()
  if (p) await onInstall(p)
}

async function onUninstallDetail() {
  const p = detailAction()
  if (p) await onUninstall(p)
}

// 进度卡片在插件列表上方，操作触发后滚到卡片处，避免插件一多看不到日志
function scrollProgressIntoView() {
  nextTick(() => progressEl.value?.scrollIntoView({ behavior: 'smooth', block: 'start' }))
}

async function onInstall(p) {
  const verb = p.installed ? '升级' : '安装'
  if (!confirm(`确定要${verb}插件「${p.name}」吗？\n\n将下载插件源码、重新编译并重启 Bot（约需几分钟）。\n\n⚠️ 安装插件等于在本机执行插件代码，请确认来源可信。`)) return
  started.value = true
  scrollProgressIntoView()
  try {
    await api.installMarketplacePlugin(p.id)
    await pollStatus()
  } catch (e) {
    listError.value = e.message
  }
}

async function onUninstall(p) {
  if (!confirm(`确定要卸载插件「${p.name}」吗？将重新编译并重启 Bot。`)) return
  started.value = true
  scrollProgressIntoView()
  try {
    await api.uninstallMarketplacePlugin(p.id)
    await pollStatus()
  } catch (e) {
    listError.value = e.message
  }
}

async function onRollback() {
  if (!confirm('确定要回滚到上次插件操作前的版本吗？将恢复旧二进制并重启 Bot。')) return
  started.value = true
  scrollProgressIntoView()
  try {
    await api.rollbackMarketplace()
    await pollStatus()
  } catch (e) {
    listError.value = e.message
  }
}

async function pollStatus() {
  try {
    const s = await api.getMarketplaceStatus()
    const wasRunning = status.running
    Object.assign(status, s)
    if (s.running) started.value = true
    if (started.value && (wasRunning || s.phase === 'done') && !s.running && s.phase === 'done' && !rebooting.value) {
      waitReboot()
    }
    nextTick(scrollLog)
  } catch { /* 服务可能正在重启 */ }
}

async function waitReboot() {
  rebooting.value = true
  const up = await api.waitUntilUp(120000)
  if (up) {
    location.reload()
  } else {
    rebooting.value = false
    alert('等待重启超时，请检查 Bot 运行状态')
  }
}

function scrollLog() {
  if (logEl.value) logEl.value.scrollTop = logEl.value.scrollHeight
}

async function onOAuthStart() {
  listError.value = ''
  try {
    const d = await api.startMarketplaceOAuth()
    Object.assign(oauth, { status: 'pending', user_code: d.user_code, verification_uri: d.verification_uri, expires_at: d.expires_at, error: '', user: '' })
    oauthOpen.value = true
    startOAuthPoll()
  } catch (e) {
    listError.value = e.message
  }
}

function startOAuthPoll() {
  stopOAuthPoll()
  oauthTimer = setInterval(pollOAuthStatus, 2000)
}

function stopOAuthPoll() {
  if (oauthTimer) {
    clearInterval(oauthTimer)
    oauthTimer = null
  }
}

async function pollOAuthStatus() {
  try {
    const s = await api.getMarketplaceOAuthStatus()
    oauth.status = s.status || ''
    oauth.user = s.user || ''
    oauth.error = s.error || ''
    if (s.status === 'authorized') {
      stopOAuthPoll()
      await loadInfo()
      setTimeout(() => { oauthOpen.value = false }, 1200)
    } else if (s.status === 'expired' || s.status === 'failed') {
      stopOAuthPoll()
    }
  } catch { /* 忽略 */ }
}

async function onOAuthCancel() {
  try { await api.cancelMarketplaceOAuth() } catch { /* 忽略 */ }
  stopOAuthPoll()
  oauthOpen.value = false
}

function copyOAuthCode() {
  if (navigator.clipboard) navigator.clipboard.writeText(oauth.user_code)
}

function phaseIndex(key) {
  if (key === 'done' || key === 'restart') return phaseOrder.length
  return phaseOrder.indexOf(key)
}
function phaseDone(key) {
  return phaseIndex(status.phase) > phaseIndex(key)
}
function phaseClass(key) {
  if (status.error && status.phase === key) return 'border-red-400 text-[#ff3b30]'
  if (phaseDone(key)) return 'border-[#34c759]/40 bg-[#34c759] text-white'
  if (status.phase === key) return 'border-zinc-900 text-zinc-900'
  return 'border-zinc-300 text-zinc-400'
}
function phaseTextClass(key) {
  if (status.error && status.phase === key) return 'text-[#ff3b30] font-semibold'
  if (status.phase === key && status.running) return 'text-zinc-900 font-semibold'
  if (phaseDone(key)) return 'text-zinc-600'
  return 'text-zinc-400'
}
function logLineClass(l) {
  if (l.startsWith('✗')) return 'text-red-400'
  if (l.startsWith('==')) return 'text-zinc-100 font-semibold mt-2'
  if (l.startsWith('$')) return 'text-zinc-500'
  return ''
}
</script>
