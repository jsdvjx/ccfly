// pre.test.ts — 唯一解析器 preFrame 的金样测试(state-engine.md §7 / F6 的落点)。
//
// 绿色用例 = 当前已正确的行为(锁住,防回归)。
// `it.fails` 用例 = 失败神谕里【尚未兑现】的修复:断言「应有的行为」,当前会抛 → vitest 记为「预期失败」
//   (套件保持绿)。等谁真把那条 F 修了,断言不再抛,`it.fails` 翻红,提醒去掉 `.fails` —— 可执行的 TODO。
import { describe, it, expect } from 'vitest'
import { preFrame } from '../pre'
import { frameFromLines } from './frameBuilder'

describe('preFrame — 选项抽取', () => {
  it('带编号单选:❯ 字形标出 cur', () => {
    const f = frameFromLines([
      'Select model',
      '',
      '  1. Opus',
      '❯ 2. Sonnet',
      '  3. Haiku',
      '',
      'esc to cancel · enter to confirm',
    ])
    const pre = preFrame(f)
    expect(pre.options.map((o) => o.num)).toEqual([1, 2, 3])
    expect(pre.options.map((o) => o.cur)).toEqual([false, true, false])
    expect(pre.options[1]?.label).toBe('Sonnet')
    expect(pre.title).toBe('Select model')
  })

  it('F1:无 ❯ 字形、仅反显高亮的行,仍标出 cur', () => {
    const f = frameFromLines([
      'Do you want to proceed?',
      { text: '  1. Yes' },
      { text: '  2. No', inverse: true }, // 无 ❯,纯反显高亮(旧实现会整菜单丢 cur)
      'esc to cancel',
    ])
    expect(preFrame(f).options.map((o) => o.cur)).toEqual([false, true])
  })

  it('F1:无字形、仅「非默认底色」高亮的行,也标出 cur', () => {
    const f = frameFromLines([
      '  1. Allow',
      { text: '  2. Deny', bgDefault: false },
      'enter to confirm',
    ])
    expect(preFrame(f).options[1]?.cur).toBe(true)
  })

  it('复选框三态 → checked(实心/勾=true、空框=false)', () => {
    const f = frameFromLines([
      '  1. ◉ Read',
      '  2. ◯ Write',
      '  3. [x] Exec',
      'space to select · enter to confirm',
    ])
    expect(preFrame(f).options.map((o) => o.checked)).toEqual([true, false, true])
  })
})

describe('preFrame — 帧级特征', () => {
  it('识别 footer / busy / inputBox', () => {
    expect(preFrame(frameFromLines(['✶ Forging… (esc to interrupt)'])).isBusy).toBe(true)
    expect(preFrame(frameFromLines(['────────────────', '❯ ', '? for shortcuts'])).inputBox).toBe(true)
    expect(preFrame(frameFromLines(['  1. A', '  2. B', 'press enter to confirm'])).footer).toMatch(/enter/i)
  })
})

describe('preFrame — effort(F7:逐字保留,不套固定 5 档)', () => {
  it('逐字捕获力度短语', () => {
    expect(preFrame(frameFromLines(['◉ medium effort  ←/→ to adjust'])).effort).toContain('medium effort')
  })
  it('非标准档位也原样保留(不被强行归一到固定阶梯)', () => {
    expect(preFrame(frameFromLines(['◈ ultrathink effort  ←/→ to adjust'])).effort).toContain('ultrathink effort')
  })
})

// ── 红色:文档化 state-engine.md 仍未兑现的失败神谕 ──
describe('KNOWN GAPS(失败神谕尚未被杀)', () => {
  it.fails('F4:无编号选项也应被解析(当前 RE_OPT 强制 \\d+[.)],裸选项被丢)', () => {
    const f = frameFromLines(['Do you want to make this edit?', '❯ Yes', '  No', 'esc to cancel'])
    expect(preFrame(f).options.length).toBe(2)
  })

  it.fails('F4:换行的长标签应 de-wrap 成一个逻辑选项(当前第二行不匹配 → 截断)', () => {
    const f = frameFromLines([
      '  1. Yes, and do not ask again for',
      '     bash commands in this session',
      '  2. No',
      'enter to confirm',
    ])
    const opt1 = preFrame(f).options[0]
    expect(opt1?.label).toContain('this session')
    expect(opt1?.rows.length).toBe(2)
  })

  it.todo('§4:cur 多见证冲突(字形说 A、反显说 B)必须被标记,以便提交时 fail-closed 兜底')
})
