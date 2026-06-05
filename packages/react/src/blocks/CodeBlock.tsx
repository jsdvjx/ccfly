// 批次1 · 「无行号散文代码块」(Markdown 正文内的围栏代码块)。前缀 cb-。
// 与 CodeCanvas(带行号文件预览)分工:本组件无行号、给散文阅读用。
// 头部条:左 lang 标签、右 自绘复制按钮 + toast;正文 shiki github-dark,未就绪/未知语言回退纯 pre。
// 长内容(超约 24 行)底部「⤢ 全屏阅读」→ 基座 openReader(text, lang)。
import { useEffect, useState } from 'react'
import { highlighter, LANG_SET } from '../highlight'
import { openReader } from './shell'

const FULLSCREEN_THRESHOLD = 24

export interface CodeBlockProps {
  code: string
  lang?: string
}

export function CodeBlock({ code, lang = '' }: CodeBlockProps) {
  const known = !!lang && LANG_SET.has(lang)
  // 高亮结果连同来源 code/lang 一起缓存,渲染期比对 → 陈旧高亮自动失效(无需在 effect 里同步清 state)。
  const [hi, setHi] = useState<{ code: string; lang: string; html: string } | null>(null)
  const [toast, setToast] = useState(false)

  useEffect(() => {
    if (!known) return
    let alive = true
    highlighter()
      .then((h) => {
        if (!alive) return
        setHi({ code, lang, html: h.codeToHtml(code, { lang, theme: 'github-dark' }) })
      })
      .catch(() => {})
    return () => {
      alive = false
    }
  }, [code, lang, known])

  const html = hi && hi.code === code && hi.lang === lang ? hi.html : ''
  const n = code.replace(/\n$/, '').split('\n').length
  const showFull = n > FULLSCREEN_THRESHOLD

  const copy = () => {
    navigator.clipboard?.writeText(code).then(
      () => {
        setToast(true)
        setTimeout(() => setToast(false), 1400)
      },
      () => {},
    )
  }

  return (
    <div className="cb">
      <div className="cb-bar">
        <span className="cb-lang">{lang || 'text'}</span>
        <span className="cb-spacer" />
        <button className="cb-copy" onClick={copy} aria-label="复制">
          {toast ? '已复制' : '复制'}
        </button>
      </div>
      <div className="cb-scroll">
        {html ? (
          <div className="cb-code" dangerouslySetInnerHTML={{ __html: html }} />
        ) : (
          <pre className="cb-code">
            <code>{code}</code>
          </pre>
        )}
      </div>
      {showFull && (
        <button className="cb-full" onClick={() => openReader(code, lang)}>
          ⤢ 全屏阅读
        </button>
      )}
    </div>
  )
}
