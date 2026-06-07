// AttachmentBar —— 图片/文件附件条:贴在 ComposeChips 之下、textarea 之上的一排 40x40 缩略图 chip。
//
// 目的(图片/文件上传 MVP):
//   让表世界用户能把图片/文件喂给里世界 Claude —— 三个入口:
//     (a) 📎 文件选择器(<input type=file multiple capture>,移动端唤起相机/相册);
//     (b) 粘贴(ControlBar 把 textarea 的 onPaste 接到 add:剪贴板里的 image/* 直接成附件);
//     (c) 拖拽(ControlBar 把 compose 区的 onDragOver/onDrop 接到 add)。
//   加入即**立刻上传**(uploadFile,带进度),chip 上叠进度环 / 错误态 + 重试 / ✕ 移除。
//   上传成功拿到设备**绝对路径**;ControlBar 在 submit 漏斗里把这些路径并进下一条提交
//   (user 文本 + 每个绝对路径,见 ControlBar 注释),里世界 Claude 据路径读图/读文件。
//
// 状态归属:ControlBar 拥有附件 state(用本文件导出的 useAttachments hook),AttachmentBar 只负责渲染。
//   这样 submit 漏斗能直接读 donePaths、成功后调 reset() —— 与既有「textarea 是唯一草稿缓冲」对称。
//
// 设计约束(对齐 ComposeChips / ControlBar):
//   - 纯展示 + hook 自包含:不引 ControlBar 内部闭包;上传逻辑收在 hook 里。
//   - 移动安全:整条 flex,窄屏横向可滚(overflow-x),宽屏 wrap;chip 固定 40x40。
//   - 无 modal:错误就地显示在 chip 上 + 点 chip 重试,不弹层打断。
//   - 预判(克制版,见 ControlBar 的预判注释):📎 按钮的 hot 态由 ControlBar 计算后传入,本文件不主动扫屏。
import { useCallback, useRef, useState, type ReactNode } from 'react'
import { uploadFile } from './api'

// 单个附件项。previewUrl 仅 image/* 有(URL.createObjectURL,卸载/移除时 revoke 防泄漏)。
export interface Attachment {
  id: string
  file: File
  previewUrl?: string // image/* 的本地预览 blob URL;非图片为空(显示文件字形)
  status: 'uploading' | 'done' | 'error'
  pct: number // 0-100;不确定态(total 未知)维持上一个值
  path?: string // 成功:设备绝对路径(submit 漏斗据此并进提交)
  error?: string // 失败:简短原因(冒在 chip 上,点击重试)
}

// 附件管理 hook(ControlBar 持有):返回当前列表 + 增删/重试 + 派生量(成功路径、是否还在传)。
export interface AttachmentsHandle {
  items: Attachment[]
  add: (files: FileList | File[] | null | undefined) => void
  remove: (id: string) => void
  retry: (id: string) => void
  reset: () => void
  donePaths: string[] // 已成功上传的绝对路径(顺序 = 加入顺序),submit 漏斗并进提交用
  anyUploading: boolean // 任一项仍在上传 → ControlBar 据此 block send(别注入还没落盘的路径)
}

let seq = 0
const nextId = () => 'att-' + Date.now().toString(36) + '-' + (seq++).toString(36)

export function useAttachments(tsess: string): AttachmentsHandle {
  const [items, setItems] = useState<Attachment[]>([])
  // tsess 放 ref:上传是异步的,重试/进行中的请求要用「当前会话」,避免闭包捕获旧值。
  const tsessRef = useRef(tsess)
  tsessRef.current = tsess

  // 单项更新工具(按 id patch,引用不变项原样保留)。
  const patch = useCallback((id: string, p: Partial<Attachment>) => {
    setItems((prev) => prev.map((it) => (it.id === id ? { ...it, ...p } : it)))
  }, [])

  // 真正发起一次上传(供加入时与重试复用)。先置 uploading/pct0,进度回调刷 pct,成败落最终态。
  const startUpload = useCallback(
    (att: Attachment) => {
      patch(att.id, { status: 'uploading', pct: 0, error: undefined })
      uploadFile(att.file, tsessRef.current, (pct) => patch(att.id, { pct }))
        .then((res) => patch(att.id, { status: 'done', pct: 100, path: res.path }))
        .catch((e: unknown) => {
          // 把状态码/原因化成短文案(413→过大、409→会话离线、其它→失败),冒在 chip 上、可点重试。
          const m = e instanceof Error ? e.message : String(e)
          const friendly = /413/.test(m) ? '文件过大' : /409/.test(m) ? '会话离线' : /40[13]/.test(m) ? '未授权' : '上传失败'
          patch(att.id, { status: 'error', error: friendly })
        })
    },
    [patch],
  )

  const add = useCallback(
    (files: FileList | File[] | null | undefined) => {
      if (!files) return
      const arr = Array.from(files)
      if (arr.length === 0) return
      const fresh: Attachment[] = arr.map((file) => ({
        id: nextId(),
        file,
        // 仅 image/* 建本地预览(blob URL);其它类型用文件字形。
        previewUrl: file.type.startsWith('image/') ? URL.createObjectURL(file) : undefined,
        status: 'uploading' as const,
        pct: 0,
      }))
      setItems((prev) => [...prev, ...fresh])
      // 加入即上传(每项独立,进度互不干扰)。
      fresh.forEach(startUpload)
    },
    [startUpload],
  )

  const remove = useCallback((id: string) => {
    setItems((prev) => {
      const it = prev.find((x) => x.id === id)
      if (it?.previewUrl) URL.revokeObjectURL(it.previewUrl) // revoke blob,防泄漏
      return prev.filter((x) => x.id !== id)
    })
  }, [])

  const retry = useCallback(
    (id: string) => {
      // 从最新列表里取该项(用函数式读取避免闭包陈旧),仅 error 态才重传。
      setItems((prev) => {
        const it = prev.find((x) => x.id === id)
        if (it && it.status === 'error') startUpload(it)
        return prev
      })
    },
    [startUpload],
  )

  const reset = useCallback(() => {
    setItems((prev) => {
      for (const it of prev) if (it.previewUrl) URL.revokeObjectURL(it.previewUrl) // 全部 revoke
      return []
    })
  }, [])

  const donePaths = items.filter((it) => it.status === 'done' && it.path).map((it) => it.path as string)
  const anyUploading = items.some((it) => it.status === 'uploading')

  return { items, add, remove, retry, reset, donePaths, anyUploading }
}

// ── 预判:里世界 Claude 是否「在要图片」(克制版,见 ControlBar 的预判注释)──
// 评审警告假阳性,故用一条 TIGHT 正则,且只在 input 态、对最近 ~12 行可见输出扫一次,跳过代码围栏行
// (代码块里出现 "screenshot" 这类词不是在「向用户要图」)。命中 → ControlBar 让 📎 按钮 .is-hot 微亮,
// 仅视觉轻推,绝不弹 modal、不打断。
// 模式刻意收紧:截图/上传图/「附上…图」/"paste the image"/"send … image"(中间≤12字)等明确「要图」措辞。
const RE_WANT_IMG = /\b(screenshot|截图|上传图|paste the image|send .{0,12}image|附上.*图)\b/i

// scanWantsImage —— 扫最近 ~12 行可见文本,跳过代码围栏内的行,任一行命中 RE_WANT_IMG 即 true。
// lines:调用方给的「最近可见输出」(ControlBar 从消息流末条 assistant 文本切行喂入)。
export function scanWantsImage(lines: string[]): boolean {
  const tail = lines.slice(-12)
  let inFence = false
  for (const raw of tail) {
    const line = raw ?? ''
    // ``` 围栏开关:围栏行本身与其内部行都跳过(代码里的关键词不算「要图」)。
    if (/^\s*```/.test(line)) {
      inFence = !inFence
      continue
    }
    if (inFence) continue
    if (RE_WANT_IMG.test(line)) return true
  }
  return false
}

// 文件大小短格式(展示在非图片 chip 的副标:1.2k / 3.4M)。
function fmtSize(n: number): string {
  if (n < 1024) return n + 'B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(n < 10 * 1024 ? 1 : 0) + 'k'
  return (n / 1024 / 1024).toFixed(1) + 'M'
}

// AttachmentBar —— 纯展示:把 useAttachments 的 items 渲成 40x40 chip 行。空列表不占位(返回 null)。
//   - 图片:缩略图(previewUrl);非图片:文件字形 + 扩展名/大小。
//   - uploading:半透明 + 进度环(pct);error:红边 + 点击重试;✕ 移除(右上角)。
//   - disabled:提交在飞时由 ControlBar 置真 —— 冻结整条(移除/重试都不可点),杜绝「提交进行中改动附件集
//     导致已捕获的 donePaths 与实际不符 / 新增项被随后的 reset 丢弃」的竞态(评审点名的 ATTACHMENT SUBMISSION RACE)。
export function AttachmentBar({ handle, disabled = false }: { handle: AttachmentsHandle; disabled?: boolean }): ReactNode {
  const { items, remove, retry } = handle
  if (items.length === 0) return null
  return (
    <div className={'attach-bar' + (disabled ? ' is-disabled' : '')} role="list" aria-label="附件" aria-disabled={disabled || undefined}>
      {items.map((it) => {
        const isImg = !!it.previewUrl
        const ext = (it.file.name.split('.').pop() || '').slice(0, 4).toUpperCase()
        return (
          <div
            key={it.id}
            className={'attach-chip' + (it.status === 'error' ? ' is-error' : '') + (it.status === 'uploading' ? ' is-uploading' : '')}
            role="listitem"
            title={it.status === 'error' ? (it.error || '上传失败') + ' · 点击重试' : it.file.name}
            // error 态点 chip 重试(无 modal,就地重试);其它态点击无副作用。提交在飞(disabled)时一律不可点。
            onClick={!disabled && it.status === 'error' ? () => retry(it.id) : undefined}
          >
            {isImg ? (
              <img className="attach-thumb" src={it.previewUrl} alt={it.file.name} />
            ) : (
              <div className="attach-thumb attach-glyph" aria-hidden>
                <span className="attach-ext">{ext || '📄'}</span>
                <span className="attach-sz">{fmtSize(it.file.size)}</span>
              </div>
            )}

            {/* 上传中:半透明覆盖 + 百分比(不确定态时 pct 维持上值,仍显数字)。 */}
            {it.status === 'uploading' && (
              <div className="attach-progress" aria-label={'上传中 ' + it.pct + '%'}>
                <span className="attach-pct">{it.pct}%</span>
              </div>
            )}

            {/* 失败:红角标 ↻(整 chip 也可点重试,这里给个明确字形提示)。 */}
            {it.status === 'error' && (
              <div className="attach-err" aria-label={it.error || '上传失败'}>
                ↻
              </div>
            )}

            {/* 移除:✕(stopPropagation 防触发 error-态的整 chip 重试)。提交在飞时禁用(防改动正在提交的附件集)。 */}
            <button
              className="attach-remove"
              title="移除"
              aria-label="移除附件"
              disabled={disabled}
              onClick={(e) => {
                e.stopPropagation()
                if (!disabled) remove(it.id)
              }}
            >
              ✕
            </button>
          </div>
        )
      })}
    </div>
  )
}

export default AttachmentBar
