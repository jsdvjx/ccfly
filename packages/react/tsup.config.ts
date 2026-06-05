import { defineConfig } from "tsup";

export default defineConfig({
  entry: ["src/index.ts"],
  format: ["esm", "cjs"],
  dts: true,
  sourcemap: true,
  clean: true,
  treeshake: true,
  // CSS:index.ts import 的 styles.css + blocks/blocks.css(+ LiveTerm 内 import 的 xterm.css)
  // 由 tsup/esbuild 合并产出 dist/index.css。下方 onSuccess 复制为 dist/style.css(对齐 package.json
  // 的 "./style.css" 导出与 README 的 import '@ccfly/react/style.css')。
  injectStyle: false,
  // peer / 重依赖外置:不打进产物,由消费方安装(见 package.json peer/deps)。
  external: [
    "react",
    "react-dom",
    "react/jsx-runtime",
    "@xterm/xterm",
    "@xterm/addon-fit",
    "shiki",
    "diff",
    "react-markdown",
    "remark-gfm",
    "zustand",
  ],
  // tsup 默认把 CSS 产出名跟 entry 同名(index.css)。复制成 style.css 供 ./style.css 导出。
  onSuccess: "cp dist/index.css dist/style.css 2>/dev/null || true",
});
