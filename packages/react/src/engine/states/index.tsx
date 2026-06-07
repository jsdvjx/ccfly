// engine/states/index.tsx — 态汇总 + 分发。
//
// import 下列各态模块,即触发它们各自的 `export const x = new XState()` 自注册(副作用);
// 同时按 kind → View 分发,供 ControlBar 一处挂载。新增一个态 = 加一行 import + 一行表项。
import type { FC } from 'react'
import { useEngineState } from '../react'
import { RichModelSelect } from './modelSelect'
import { PermissionCard } from './permission'
import { EffortCard } from './effort'
import { ConfirmCard } from './confirm'
import { MultiCard } from './multi'
import { ScopeCard } from './sessionScope'
import { ListCard } from './list'

const VIEWS: Record<string, FC> = {
  modelSelect: RichModelSelect,
  permission: PermissionCard,
  effort: EffortCard,
  confirm: ConfirmCard,
  multi: MultiCard,
  sessionScope: ScopeCard,
  list: ListCard,
}

// 引擎当前命中某个 select 态 → 渲对应卡片;未命中(null)→ 交回 ControlBar 既有分支。
export function EngineControl() {
  const m = useEngineState()
  if (!m) return null
  const View = VIEWS[m.kind]
  return View ? <View /> : null
}
