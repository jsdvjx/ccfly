import type { ComponentType } from 'react'

// 全卡唯一契约。每个 info/*.tsx 导出 parse + Card({data}),并在末尾聚合成一个 CardModule。
// parse:抓屏文本 → 结构 T(成功)或 null(失败,上层回退原文);兼作轮询「是否到位」校验。
// Card:渲染原生卡,prop 名恒为 data —— 消灭 {u}/{s}/{ctx}/{data} 漂移。
export interface CardModule<T> {
  parse: (text: string) => T | null
  Card: ComponentType<{ data: T }>
}
