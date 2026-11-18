import rule from './rule.js'
import { createRequire } from 'node:module'

const { version } = createRequire(import.meta.url)('../package.json')
const plugin = {
  meta: { name: 'eslint-plugin-unstring', version },
  rules: { 'closed-set-literal': rule },
  configs: {},
}
plugin.configs.recommended = {
  plugins: { unstring: plugin },
  rules: { 'unstring/closed-set-literal': 'error' },
}
export default plugin
