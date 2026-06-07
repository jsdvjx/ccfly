// engine/react.ts — React 适配:把引擎当前态接进组件(useSyncExternalStore)。
// 引擎本体(engine.ts)不依赖 React;这一层薄薄地把订阅/快照接上。
import { useSyncExternalStore } from 'react'
import { subscribe, current } from './engine'

export function useEngineState() {
  return useSyncExternalStore(subscribe, current, current)
}
