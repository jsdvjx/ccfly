// CCFlyHosts —— 把四个「模块级单例弹层 host」聚合成一个组件,消费方在 App 根挂一次。
//
// 这对应一个根 main.tsx 里与 <App/> 并列渲染的:
//   <ReaderHost/>(全屏代码阅读器)、<SubagentHost/>(子代理栈式弹层)、
//   <WorkflowOverlayHost/>(工作流详情弹层)、<LightboxHost/>(图片大图)。
//
// 这些 host 的 open* 触发器是模块级单例(openReader / openSubagent / openWorkflow / openLightbox),
// 故同一页面只需挂一份。务必挂在 <CCFlyProvider> 子树内(弹层里的子代理视图会用到注入的 config)。
//
// 用法:
//   <CCFlyProvider config={...}>
//     <SessionView sid={sid} />
//     <CCFlyHosts />
//   </CCFlyProvider>
//
// P0.5 TODO(多实例化):open* 是模块级单例 → 全页共享一组弹层栈。若要多实例隔离(多设备并排各自的
// 子代理/阅读器弹层),需把这些 store 收进 React context/Provider,open* 改为从 hook 取。P0 不做。
import { ReaderHost } from './blocks/shell'
import { SubagentHost } from './SubagentView'
import { WorkflowOverlayHost } from './blocks/WorkflowCard'
import { LightboxHost } from './blocks/ImageBlock'

export function CCFlyHosts() {
  return (
    <>
      <ReaderHost />
      <SubagentHost />
      <WorkflowOverlayHost />
      <LightboxHost />
    </>
  )
}
