# examples/web

Minimal [Vite](https://vitejs.dev/) app that consumes `@ccfly/react` to render a
local Claude Code session.

> Placeholder — to be filled in once `@ccfly/react` has its first components.

## Planned shape

```sh
# 1) start the local control service
npx ccfly                 # or: pnpm build:go && ./bin/ccfly

# 2) run this example against it
pnpm install
pnpm dev                  # Vite dev server
```

```tsx
// src/App.tsx
import { CcflySession } from "@ccfly/react";
import "@ccfly/react/style.css";

export default function App() {
  return <CcflySession endpoint="http://localhost:7777" />;
}
```

Within this monorepo the example will depend on `@ccfly/react` via the pnpm
workspace (`"@ccfly/react": "workspace:*"`).
