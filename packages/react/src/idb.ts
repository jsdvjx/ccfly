// ── transcript 全量缓存(IndexedDB)──
// 替代旧的 localStorage(jv:tx:* + 3MB 上限):IndexedDB 配额足够,大会话也整段缓存,
// 进会话时整段读出供「上滑瞬时前插」用,只渲染末尾一窗。
// 数据形态换版(图片消息从旧的「拍平成文本」改为新后端渲染),故不迁移旧 localStorage,且顺手清掉。

import type { Item } from './types'
import { getConfig } from './config'

export interface TxCache {
  items: Item[]
  cursor: number
  firstCursor: number
  hasMore: boolean
}

// IndexedDB 库名:由 storagePrefix 派生(替代硬编码库名),避免多消费方撞库。
// 去掉前缀里的尾部分隔符(如 'ccfly:' → 'ccfly')再作库名。
const DB_NAME = () => (getConfig().storagePrefix.replace(/[:_-]+$/, '') || 'ccfly')
const STORE = 'tx'
const cacheKey = (host: string, sid: string) => host + ':' + sid

let dbp: Promise<IDBDatabase | null> | null = null

function openDb(): Promise<IDBDatabase | null> {
  if (dbp) return dbp
  dbp = new Promise<IDBDatabase | null>((resolve) => {
    try {
      const req = indexedDB.open(DB_NAME(), 1)
      req.onupgradeneeded = () => {
        const db = req.result
        if (!db.objectStoreNames.contains(STORE)) db.createObjectStore(STORE)
      }
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => resolve(null)
    } catch {
      resolve(null) // 隐私模式 / 不支持:降级为无缓存
    }
  })
  return dbp
}

export async function idbGetTx(host: string, sid: string): Promise<TxCache | null> {
  const db = await openDb()
  if (!db) return null
  return new Promise<TxCache | null>((resolve) => {
    try {
      const tx = db.transaction(STORE, 'readonly')
      const req = tx.objectStore(STORE).get(cacheKey(host, sid))
      req.onsuccess = () => {
        const o = req.result
        if (o && Array.isArray(o.items) && typeof o.cursor === 'number')
          resolve({
            items: o.items,
            cursor: o.cursor,
            firstCursor: typeof o.firstCursor === 'number' ? o.firstCursor : 0,
            hasMore: !!o.hasMore,
          })
        else resolve(null)
      }
      req.onerror = () => resolve(null)
    } catch {
      resolve(null)
    }
  })
}

export async function idbPutTx(host: string, sid: string, val: TxCache): Promise<void> {
  const db = await openDb()
  if (!db) return
  return new Promise<void>((resolve) => {
    try {
      const tx = db.transaction(STORE, 'readwrite')
      tx.objectStore(STORE).put(val, cacheKey(host, sid))
      tx.oncomplete = () => resolve()
      tx.onerror = () => resolve()
      tx.onabort = () => resolve()
    } catch {
      resolve()
    }
  })
}

// 清掉旧版 localStorage 的 <prefix>tx:* 键(旧数据是图片拍平成文本的旧形态,换 IndexedDB 自然全量重取)。
// 前缀由 storagePrefix 派生(替代硬编码 'jv:tx:')。
export function purgeLegacyTx(): void {
  try {
    const legacy = getConfig().storagePrefix + 'tx:'
    const rm: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i)
      if (k && k.startsWith(legacy)) rm.push(k)
    }
    for (const k of rm) localStorage.removeItem(k)
  } catch {
    /* ignore */
  }
}
