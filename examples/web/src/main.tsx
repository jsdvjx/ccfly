// 入口:挂载 App。@ccfly/react 的样式必须在此引入(库提示:不要重复 import xterm.css)。
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@ccfly/react/style.css'
import './index.css'
import { App } from './App'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
