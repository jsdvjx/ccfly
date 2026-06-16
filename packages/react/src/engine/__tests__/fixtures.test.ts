// fixtures.test.ts — 用【真机抓屏】夹具喂引擎(state-engine.md §7 / F6 的「capture real frames」)。
//
// 夹具取自一个受控 claude 会话(cc-fxtest @70 列)的真实 `tmux capture-pane`,非手搓。
// 绿色用例锁住「当前已对」的行为;`it.fails` 把真机暴露的缺陷写成可执行 TODO(修好即翻红)。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { classify } from '../engine'
import { preFrame } from '../pre'
import { frameFromLines } from './frameBuilder'
import '../states' // 注册各 resolve

function loadFixture(name: string) {
  const txt = readFileSync(new URL(`./fixtures/${name}`, import.meta.url), 'utf8')
  return frameFromLines(txt.replace(/\n$/, '').split('\n'))
}

describe('真机 /model 菜单(cc-fxtest @70 列,选项描述跨行)', () => {
  const frame = loadFixture('model-select.txt')

  it('分类正确:modelSelect(不被 list/effort 抢)', () => {
    expect(classify(frame)?.kind).toBe('modelSelect')
  })

  it('五个模型选项,编号 1..5,cur 落在 Default(❯)', () => {
    const pre = preFrame(frame)
    expect(pre.options.map((o) => o.num)).toEqual([1, 2, 3, 4, 5])
    expect(pre.options[0]?.cur).toBe(true)
  })

  it('F7:effort 短语原样保留(xHigh,非标准档,不套固定阶梯)', () => {
    expect(preFrame(frame).effort).toContain('xHigh effort')
  })

  // 标题取「文字块最顶行」,不再取描述段尾行(本提交修复;真机夹具锁回归)。
  it('标题取真标题 "Select model"(而非描述段尾行 "--model.")', () => {
    expect(preFrame(frame).title).toBe('Select model')
  })

  // ── 真机仍暴露的缺陷(尚未修)──
  it.fails('F4:跨行的选项描述应 de-wrap;option1 标签应含 "everyday",当前被截断在首行', () => {
    expect(preFrame(frame).options[0]?.label).toContain('everyday')
  })
})
