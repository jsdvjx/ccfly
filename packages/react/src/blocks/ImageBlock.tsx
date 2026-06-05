// 用户消息里的图片:VSCode 式缩略 chip(小缩略 + 文件名 + 尺寸 W×H),点击看大图。
// 不内联 base64:src 走后端 /image(按 item.uuid + block.imgIdx 取字节)。lightbox 走模块级
// store + LightboxHost(挂 App 根渲染一次)+ openLightbox 触发 —— 照 shell.tsx 的 openReader 模式。
// 弹层铁律:不点外关、只 ✕ 关 + Esc 关;深色紧凑风。前缀 ic-(chip) / lb-(lightbox)。
import { createContext, useContext, useEffect, useState, useSyncExternalStore } from 'react'
import { imageUrl } from '../api'
import { useSession } from './ctx'
import type { Block } from '../types'

// 路径式取 basename 作文件名;base64 式无路径 → image.<ext>(从 mediaType 推扩展)。
function imageName(block: Block): string {
  if (block.path) {
    const parts = block.path.split('/')
    return parts[parts.length - 1] || 'image'
  }
  const mt = block.mediaType || 'image/png'
  const ext = mt.split('/')[1] || 'png'
  return 'image.' + ext
}

// ── 图片 chip:小缩略 + 文件名 + 尺寸(图载入后用 naturalWidth×naturalHeight)──
export function ImageChip({ block }: { block: Block }) {
  const { host, sid } = useSession()
  const [dim, setDim] = useState<{ w: number; h: number } | null>(null)
  const [err, setErr] = useState(false)
  const uuid = useImageUuid()
  const idx = block.imgIdx ?? 0
  const name = imageName(block)
  // uuid 缺失(老条目/子代理无 uuid)→ 无法取字节,降级为纯文件名 chip(不挂 src)。
  const src = host && sid && uuid ? imageUrl(host, sid, uuid, idx) : ''

  return (
    <button
      className={'ic-chip' + (err ? ' ic-chip--err' : '')}
      onClick={src && !err ? () => openLightbox(src, name) : undefined}
      title={block.path || name}
    >
      <span className="ic-thumb">
        {src && !err ? (
          <img
            src={src}
            alt={name}
            loading="lazy"
            onLoad={(e) => setDim({ w: e.currentTarget.naturalWidth, h: e.currentTarget.naturalHeight })}
            onError={() => setErr(true)}
          />
        ) : (
          <span className="ic-ph">🖼</span>
        )}
      </span>
      <span className="ic-meta">
        <span className="ic-name">{name}</span>
        <span className="ic-dim">{err ? '加载失败' : dim ? dim.w + '×' + dim.h : '图片'}</span>
      </span>
    </button>
  )
}

// 用户消息条目的 uuid 经 ImageUuidContext 注入(消息壳在渲染图片块前 Provider)。
const ImageUuidContext = createContext<string>('')
export const ImageUuidProvider = ImageUuidContext.Provider
function useImageUuid(): string {
  return useContext(ImageUuidContext)
}

// ── lightbox:模块级 store + LightboxHost + openLightbox 触发(照 openReader 模式)──
interface LightboxItem {
  src: string
  name: string
}
let lbState: LightboxItem | null = null
const lbSubs = new Set<() => void>()
function lbEmit() {
  for (const fn of lbSubs) fn()
}
export function openLightbox(src: string, name: string) {
  lbState = { src, name }
  lbEmit()
}
function closeLightbox() {
  lbState = null
  lbEmit()
}
function lbSubscribe(fn: () => void) {
  lbSubs.add(fn)
  return () => {
    lbSubs.delete(fn)
  }
}
function lbSnapshot() {
  return lbState
}

// 挂载一次:在 main.tsx 与 <App/> 并列。打开时全屏覆盖看大图;不点外关、✕/Esc 关。
export function LightboxHost() {
  const item = useSyncExternalStore(lbSubscribe, lbSnapshot, lbSnapshot)
  useEffect(() => {
    if (!item) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeLightbox()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [item])
  if (!item) return null
  return (
    <div className="lb">
      <div className="lb-bar">
        <span className="lb-name">{item.name}</span>
        <span className="lb-spacer" />
        <button className="lb-close" onClick={closeLightbox} aria-label="关闭">
          ✕
        </button>
      </div>
      <div className="lb-scroll">
        <img className="lb-img" src={item.src} alt={item.name} />
      </div>
    </div>
  )
}
