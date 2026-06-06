// config.ts —— 参数化配置层(取代上游里硬编码的 /x/<host>、/d/<host>/7681、jv: 前缀)。
//
// 设计(P0):
//   消费方在 React 树顶层包一个 <CCFlyProvider config={...}/>(见 CCFlyProvider.tsx),它会把这份配置
//   同步进一个「模块级单例」(setConfig)。api.ts / ttyd.ts / idb.ts / sendkeys.ts 等纯函数层从这个
//   单例读取(getConfig),无需把 baseUrl 一路 props 钻孔。
//
// 为什么是模块级单例而非纯 React context:
//   - api.ts 的很多函数在 React 渲染之外被调用(SSE/WS 回调、sendkeys.ts、idb 防抖写回、模块级
//     openReader/openSubagent 等单例 host)。这些地方拿不到 React context。
//   - P0 目标:单实例消费可编译可用。模块级单例满足之。
//
// P0.5 TODO(多实例化):
//   模块级单例意味着同一页面只能有一个「控制服务端点」。若将来要在一个页面里同时挂多个指向不同
//   baseUrl 的 SessionView(多设备并排),需把 config 收进 React context 并让 api/ttyd 改为「从 hook
//   取 client 实例」的工厂式调用(api 函数都加一个 client 参数,或返回绑定了 config 的方法对象)。
//   届时模块级单例降级为「默认实例」兜底。现在先不做,保持改动面最小、组件签名零变更。

export interface CCFlyConfig {
  // 控制服务前缀:替代硬编码的 `/x/<host>`。所有 REST(transcript/state/sendkeys/…)拼在它后面。
  // 形如 '/x/mac' 或 'https://hub.example.com/x/mac'。不含结尾斜杠。
  baseUrl: string
  // ccfly 自带终端 WebSocket 的 base 前缀(不含 '/term')。ccfly 的 Go 服务自己提供 /term
  // (PTY + tmux,ttyd 帧兼容),**不再依赖外部 ttyd**。LiveTerm/ttyd.ts 在其后追加
  // '/term?session=<tmux 会话名>'(可选 &cwd=&cmd=)。
  // 形如 '/x/mac'(同源相对)或 'wss://hub/x/mac'(绝对);通常与 baseUrl 同值。
  // 空字符串 → 同源(协议跟随页面)直连 /term。缺省由 baseUrl 派生(见 CCFlyProvider)。
  wsBaseUrl: string
  // 会话列表接口:替代硬编码的 `/api/claude-sessions`。可空(空 → fetchSessions 返回 [])。
  sessionsUrl?: string
  // 自定义 fetch(SSR / 鉴权注入 / 测试桩);缺省用全局 fetch。
  fetch: typeof fetch
  // 由 sid 推 tmux 会话名;替代硬编码的 'cc-' + sid.slice(0,8)。
  tmuxName: (sid: string) => string
  // 「打开终端(新标签)」的 URL;降级(LiveTerm WS 连不上)时用。
  // 默认无外部终端可指向(ccfly 自带 /term 是 WS、不是可直接开标签页的 HTML),返回空串表示「无直链」;
  // 消费方若另起了网页终端(如自托管 ttyd)可覆盖此项。
  terminalUrl: (sid: string, cwd?: string) => string
  // 由 sid 推 resume 命令(LiveTerm 首次创建该 tmux 会话时,作为 /term 的 ?cmd= 透传给 tmux)。
  resumeCmd: (sid: string) => string
  // localStorage / sessionStorage / IndexedDB 键前缀;替代硬编码的 'jv:' 前缀 / 库名。
  storagePrefix: string
}

// 默认配置:同源直连本地 ccfly 服务(默认 /),终端走 ccfly 自带的 /term(无外部 ttyd)。
// host 固定占位 'mac',方便单测/示例不传也能跑。真实消费方应通过 <CCFlyProvider> 覆盖。
function defaultConfig(): CCFlyConfig {
  const host = 'mac'
  return {
    baseUrl: '/x/' + host,
    wsBaseUrl: '/x/' + host, // 与 baseUrl 同源;ttyd.ts 在其后拼 /term?session=…
    sessionsUrl: '/api/claude-sessions',
    fetch: (...a: Parameters<typeof fetch>) => globalThis.fetch(...a),
    tmuxName: (sid) => 'cc-' + sid.slice(0, 8),
    // 默认无外部终端直链(ccfly /term 是 WS,不可直接开新标签);降级时 UI 据空串隐藏「打开终端」。
    terminalUrl: () => '',
    // 经用户交互登录 shell 跑 claude(加载 ~/.zshrc 的 PATH,claude 常在 ~/.local/bin);详见 CCFlyProvider.resolve。
    resumeCmd: (sid) => `$SHELL -ilc 'claude --resume ${sid}'`,
    storagePrefix: 'ccfly:',
  }
}

let current: CCFlyConfig = defaultConfig()

// Provider 在挂载/配置变化时调用,写入模块级单例。
export function setConfig(cfg: CCFlyConfig): void {
  current = cfg
}

// 纯函数层(api/ttyd/idb/sendkeys)读取当前生效配置。
export function getConfig(): CCFlyConfig {
  return current
}

// 便捷:带前缀的存储键(localStorage/sessionStorage)。把上游里 'jv:scroll:'、'jv:verb:'、
// 'jv:effort:' 这类键统一改成 storagePrefix + 子键。
export function storageKey(suffix: string): string {
  return getConfig().storagePrefix + suffix
}
