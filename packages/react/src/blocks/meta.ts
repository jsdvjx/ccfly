// 批次0 基座 · 纯数据/函数(不渲染)。各工具卡共享的元信息、文件图标、语言映射、brief 提取。
import type { Accent } from './shell'
import { LANG_SET } from '../highlight'

export interface ToolMeta {
  icon: string
  title: string
  accent: Accent
  defaultOpen: boolean
}

// 工具元信息表。未命中的工具由调用方回退到 unknown/none。
// defaultOpen 对齐 claude TUI 折叠习惯:
//   · 读类(Read/Grep/Glob/LS)默认折叠(紧凑头 + 正文截断预览,点开看全);
//   · 写类(Write/Edit/MultiEdit/NotebookEdit)默认折叠(头部一行给路径 + ± 统计;点开看 TUI 式 diff/全文);
//   · Plan/Task 默认展开(计划/子代理活动直接看);Bash 默认折叠;
//   · thinking 折叠(各卡自带);TodoWrite 自渲不走基座(none)。
export const TOOL_META: Record<string, ToolMeta> = {
  // 文件(file)·读类与写类均折叠(写类折叠头露路径 + 统计,展开露 diff/全文)
  Read: { icon: '📖', title: 'Read', accent: 'file', defaultOpen: false },
  Write: { icon: '✏️', title: 'Write', accent: 'file', defaultOpen: false },
  Edit: { icon: '✂️', title: 'Edit', accent: 'file', defaultOpen: false },
  MultiEdit: { icon: '✂️', title: 'MultiEdit', accent: 'file', defaultOpen: false },
  NotebookEdit: { icon: '📓', title: 'NotebookEdit', accent: 'file', defaultOpen: false },
  NotebookRead: { icon: '📓', title: 'NotebookRead', accent: 'file', defaultOpen: false },
  // 执行/搜索(exec)·默认折叠(头部常显 + 正文截断预览)
  Bash: { icon: '❯', title: 'Bash', accent: 'exec', defaultOpen: false },
  BashOutput: { icon: '❯', title: 'BashOutput', accent: 'exec', defaultOpen: false },
  KillShell: { icon: '✕', title: 'KillShell', accent: 'exec', defaultOpen: false },
  SlashCommand: { icon: '⌘', title: 'SlashCommand', accent: 'exec', defaultOpen: false },
  Grep: { icon: '🔍', title: 'Grep', accent: 'exec', defaultOpen: false },
  Glob: { icon: '🗂', title: 'Glob', accent: 'exec', defaultOpen: false },
  LS: { icon: '🗂', title: 'LS', accent: 'exec', defaultOpen: false },
  // 清单(none,自渲)
  TodoWrite: { icon: '📋', title: 'Todos', accent: 'none', defaultOpen: true },
  // 子代理(task)·默认展开
  Task: { icon: '🤖', title: 'Task', accent: 'task', defaultOpen: true },
  Agent: { icon: '🤖', title: 'Agent', accent: 'task', defaultOpen: true },
  // 计划(plan)·默认展开
  ExitPlanMode: { icon: '📝', title: 'Plan', accent: 'plan', defaultOpen: true },
  // 技能(skill)·默认折叠
  Skill: { icon: '⚡', title: 'Skill', accent: 'skill', defaultOpen: false },
  // 网络(web)·默认折叠(失败再自动展开,见各卡)
  WebFetch: { icon: '🌐', title: 'WebFetch', accent: 'web', defaultOpen: false },
  WebSearch: { icon: '🌐', title: 'WebSearch', accent: 'web', defaultOpen: false },
}

export interface FileIcon {
  glyph: string
  color: string
}

// 文件图标:按扩展名给一个字形 + 颜色(对齐 CSS 变量族色)。二进制/未知给占位。
export function fileIcon(path: string): FileIcon {
  const ext = extOf(path)
  switch (ext) {
    case 'ts':
    case 'tsx':
    case 'js':
    case 'jsx':
    case 'mjs':
    case 'cjs':
      return { glyph: '◇', color: '#4f9cf9' } // 蓝
    case 'go':
      return { glyph: '◈', color: '#4dd0e1' } // 青
    case 'css':
    case 'scss':
    case 'less':
      return { glyph: '◆', color: '#b794f6' } // 紫
    case 'json':
    case 'yaml':
    case 'yml':
    case 'toml':
      return { glyph: '⚙', color: '#ffcf6b' } // 黄
    case 'md':
    case 'mdx':
    case 'txt':
      return { glyph: '▤', color: '#e6e8eb' } // 灰白
    case 'py':
      return { glyph: '◐', color: '#3ddc84' } // 绿
    case 'sh':
    case 'bash':
    case 'zsh':
    case 'fish':
      return { glyph: '❯', color: '#8b93a1' }
    case 'ipynb':
      return { glyph: '◉', color: '#ff9f6b' } // 橙
    case 'html':
    case 'htm':
      return { glyph: '◇', color: '#ffcf6b' }
    case 'rs':
      return { glyph: '◈', color: '#ff9f6b' }
    case 'sql':
      return { glyph: '⛁', color: '#4dd0e1' }
    case 'png':
    case 'jpg':
    case 'jpeg':
    case 'gif':
    case 'webp':
    case 'svg':
    case 'ico':
      return { glyph: '▦', color: '#b794f6' } // 图片
    case 'pdf':
    case 'zip':
    case 'tar':
    case 'gz':
    case 'wasm':
    case 'bin':
      return { glyph: '▪', color: '#6b7280' } // 二进制占位
    default:
      return { glyph: '▢', color: '#8b93a1' } // 其它/未知占位
  }
}

// 文件路径 → shiki lang(对齐 highlight.ts 的 LANG_SET;未知返回空字符串)。
export function langOf(path: string): string {
  const ext = extOf(path)
  const map: Record<string, string> = {
    ts: 'ts',
    tsx: 'tsx',
    js: 'js',
    jsx: 'jsx',
    mjs: 'js',
    cjs: 'js',
    json: 'json',
    go: 'go',
    py: 'python',
    sh: 'bash',
    bash: 'bash',
    zsh: 'bash',
    html: 'html',
    htm: 'html',
    css: 'css',
    scss: 'css',
    md: 'md',
    mdx: 'md',
    diff: 'diff',
    patch: 'diff',
    yaml: 'yaml',
    yml: 'yaml',
    toml: 'toml',
    rs: 'rust',
    sql: 'sql',
    dockerfile: 'dockerfile',
  }
  const lang = map[ext] || ''
  return lang && LANG_SET.has(lang) ? lang : ''
}

// 工具入参 → 一行 brief(头部副标题)。按优先级取首个非空字符串字段;最后兜底首个短 string。
export function briefOf(input: Record<string, unknown> | undefined): string {
  if (!input) return ''
  const order = [
    'command',
    'description',
    'pattern',
    'query',
    'path',
    'file_path',
    'notebook_path',
    'url',
    'name',
    'title',
  ]
  for (const k of order) {
    const v = input[k]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  // 兜底:首个较短的 string 字段(避免把整段 content 当 brief)。
  for (const k of Object.keys(input)) {
    const v = input[k]
    if (typeof v === 'string' && v.trim() && v.length <= 120) return v.trim()
  }
  return ''
}

// ── 内部:取小写扩展名(对 Dockerfile 这类无扩展名按文件名识别)──
function extOf(path: string): string {
  if (!path) return ''
  const base = path.split('/').pop() || path
  if (/^dockerfile$/i.test(base)) return 'dockerfile'
  if (/^makefile$/i.test(base)) return 'makefile'
  const dot = base.lastIndexOf('.')
  if (dot <= 0) return ''
  return base.slice(dot + 1).toLowerCase()
}
