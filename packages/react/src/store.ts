import { create } from 'zustand'
import type { Item, PatchHunk } from './types'

type ResultMap = Record<string, { content: string; isError: boolean; patch?: PatchHunk[] }>

export function indexResults(items: Item[], into: ResultMap) {
  for (const it of items)
    for (const b of it.blocks || [])
      if (b.type === 'tool_result' && b.forId)
        into[b.forId] = { content: b.content || '', isError: !!b.isError, patch: b.patch }
}

// 去重键:后端 tItem 不带 jsonl uuid(只有 role/kind/text/ts/blocks),
// 故用「可前端计算的复合键」兜底——role|ts|kind|首块类型|正文前 120 字符。
// 三路汇合(缓存秒显 + since 增量 + SSE 跟随)在游标边界/断线重连/缓存重叠时可能把同一条 jsonl
// 重复塞入;同一 jsonl 条目这五项必然一致,而不同消息(哪怕同 ts)几乎不可能五项全同 → 既去重又不误杀。
export function itemKey(it: Item): string {
  const b0 = it.blocks && it.blocks[0]
  const bType = b0 ? b0.type : ''
  const body = (it.text || '').slice(0, 120)
  return it.role + '|' + (it.ts || '') + '|' + (it.kind || '') + '|' + bType + '|' + body
}

interface State {
  items: Item[]
  cursor: number
  // 渲染集里最旧 item 所在行的起始字节(向上分页用 before=firstCursor);0=已到顶/未知。
  firstCursor: number
  // 是否还有更老的 item(供上滑判断)。
  hasMore: boolean
  resultById: ResultMap
  seen: Set<string> // 已见去重键集合(随 items 同步维护)
  reset: () => void
  // 首拉/全量重建:窗口 items + 后向游标 cursor + 向上分页 firstCursor/hasMore。
  setInitial: (items: Item[], cursor: number, firstCursor?: number, hasMore?: boolean) => void
  append: (cursor: number, item: Item) => void
  appendMany: (items: Item[], cursor: number) => void
  // 上滑前插:更老一窗插到头部(逐条按 itemKey 去重),并更新 firstCursor/hasMore。
  prependMany: (items: Item[], firstCursor: number, hasMore: boolean) => void
}

export const useStore = create<State>((set) => ({
  items: [],
  cursor: 0,
  firstCursor: 0,
  hasMore: false,
  resultById: {},
  seen: new Set<string>(),
  reset: () => set({ items: [], cursor: 0, firstCursor: 0, hasMore: false, resultById: {}, seen: new Set<string>() }),
  // 全量重建:缓存秒显 / compact-clear 后全量重拉都走这里,顺带重建 seen 并就地去重。
  setInitial: (items, cursor, firstCursor = 0, hasMore = false) => {
    const seen = new Set<string>()
    const deduped: Item[] = []
    for (const it of items) {
      const k = itemKey(it)
      if (seen.has(k)) continue
      seen.add(k)
      deduped.push(it)
    }
    const r: ResultMap = {}
    indexResults(deduped, r)
    return set({ items: deduped, cursor, firstCursor, hasMore, resultById: r, seen })
  },
  append: (cursor, item) =>
    set((s) => {
      const k = itemKey(item)
      if (s.seen.has(k)) return { cursor: Math.max(s.cursor, cursor) } // 已见 → 只推进游标
      const seen = new Set(s.seen)
      seen.add(k)
      const r = { ...s.resultById }
      indexResults([item], r)
      return { items: [...s.items, item], cursor: Math.max(s.cursor, cursor), resultById: r, seen }
    }),
  // 批量追加(缓存命中后,用 since=游标 拉到的增量一次性接上)。逐条跳过已见键。
  appendMany: (newItems, cursor) =>
    set((s) => {
      const seen = new Set(s.seen)
      const r = { ...s.resultById }
      const fresh: Item[] = []
      for (const it of newItems) {
        const k = itemKey(it)
        if (seen.has(k)) continue
        seen.add(k)
        fresh.push(it)
      }
      if (fresh.length === 0) return { cursor: Math.max(s.cursor, cursor) }
      indexResults(fresh, r)
      return { items: [...s.items, ...fresh], cursor: Math.max(s.cursor, cursor), resultById: r, seen }
    }),
  // 上滑加载更老:逐条去重后前插到头部;firstCursor/hasMore 取传入值(本批最旧行首 + 是否还有更老)。
  prependMany: (newItems, firstCursor, hasMore) =>
    set((s) => {
      const seen = new Set(s.seen)
      const r = { ...s.resultById }
      const fresh: Item[] = []
      for (const it of newItems) {
        const k = itemKey(it)
        if (seen.has(k)) continue
        seen.add(k)
        fresh.push(it)
      }
      if (fresh.length === 0) return { firstCursor, hasMore }
      indexResults(fresh, r)
      return { items: [...fresh, ...s.items], firstCursor, hasMore, resultById: r, seen }
    }),
}))
