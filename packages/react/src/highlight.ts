import { createHighlighter, type Highlighter } from 'shiki'

let hp: Promise<Highlighter> | null = null

const LANGS = [
  'ts', 'tsx', 'js', 'jsx', 'json', 'go', 'python', 'bash', 'sh', 'shell',
  'html', 'css', 'md', 'diff', 'yaml', 'toml', 'rust', 'sql', 'dockerfile',
]

export function highlighter(): Promise<Highlighter> {
  if (!hp) hp = createHighlighter({ themes: ['github-dark'], langs: LANGS })
  return hp
}

export const LANG_SET = new Set(LANGS)
