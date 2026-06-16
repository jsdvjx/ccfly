// resolves.test.ts — 分类层(classify:按 weight 跑注册表)的金样测试,形状取自 live tmux cc-* 真机抓屏。
//
// 这一层才是「前端状态判断让人迷惑」的真正所在:不是 busy/input 这种粗分,而是 select 家族里
// 「到底是哪一种 select」的决定性信号是否互斥。下面第二组就是一条【真机复现】的渗漏(F3)。
import { describe, it, expect } from 'vitest'
import { classify } from '../engine'
import { frameFromLines } from './frameBuilder'
import '../states' // 副作用:构造各 State 子类 → 注册进 registry(与线上同一张表)

describe('classify — 真机金样(busy / idle 不应被 select 家族抢)', () => {
  it('busy(cc-b717ce86 形状):select 家族全落空 → null(引擎交回 ControlBar 的 busy 卡)', () => {
    const f = frameFromLines([
      '✻ Skedaddling… (11m 57s · ↓ 50.0k tokens)',
      '──────────────────────────────────────────',
      '❯ ',
      '──────────────────────────────────────────',
      '  ⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt',
    ])
    expect(classify(f)).toBeNull()
  })

  it('idle 输入框(cc-1a81bd0e 形状):select 家族全落空 → null(交回 input 卡)', () => {
    const f = frameFromLines([
      '──────────────────────────────────────────',
      '❯ ',
      '──────────────────────────────────────────',
      '  ⏵⏵ auto mode on · 1 shell · ← for agents · ↓ to manage',
    ])
    expect(classify(f)).toBeNull()
  })
})

describe('classify — picker 正确归 list,而非 permission(真机 cc-4cef587c)', () => {
  // 「恢复会话」三选一菜单,差一点就被 permission 抢走 —— 锁成回归边界:
  //   permission 的 RE_GRANT = /don'?t ask again|always|allow|.../(注意是 "ask again",无 "me")。
  //   真权限菜单用 "Yes, and don't ask again"(命中 → 正确归 permission)。
  //   本 resume 菜单第 3 项是 "Don't ask me again"(中间有 "me")→ 不命中 → permission 正确让位,
  //   兜底 list(weight 90)拿下。
  // 若哪天有人把 RE_GRANT 放宽到也吃 "ask me again",这条会翻红 —— 提醒别把 picker 误判成权限卡。
  const resume = () =>
    frameFromLines([
      'Resuming the full session will consume a substantial portion of your usage limits. We recommend resuming from a summary.',
      '❯ 1. Resume from summary (recommended)',
      '  2. Resume full session as-is',
      "  3. Don't ask me again",
      'Enter to confirm · Esc to cancel',
    ])

  it('恢复会话 picker → list(不是 permission)', () => {
    expect(classify(resume())?.kind).toBe('list')
  })
})
