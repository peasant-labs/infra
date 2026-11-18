import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, writeFileSync, rmSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { Linter } from 'eslint'
import parser from '@typescript-eslint/parser'
import { parse } from 'yaml'
import plugin from '../src/index.js'

const corpus = parse(readFileSync(fileURLToPath(new URL('../../internal/typed/testdata/typescript.yaml', import.meta.url)), 'utf8'))
const required = ['ts comparison', 'reversed comparison', 'switch case', 'structural twin', 'any receiver', 'string receiver', 'unknown receiver', 'inline union', 'no runtime value', 'non-member', 'js typedef frozen table', 'jsx comparison', 'closed value wire', 'mutable table is not a set', 'twin both types used', 'narrowed receiver retains declared set', 'imported role property in jsdoc', 'any property ignores same name', 'imported jsdoc parameter', 'imported structural twin without table', 'widened table members are not evidence']

test('required conformance cases are present and unique', () => {
  const names = corpus.map(item => item.name)
  assert.equal(new Set(names).size, names.length)
  for (const name of required) assert.ok(names.includes(name), `missing fixture: ${name}`)
})

test('typed conformance corpus', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'unstring-'))
  try {
    writeFileSync(join(dir, 'tsconfig.json'), JSON.stringify({ compilerOptions: { allowJs: true, checkJs: true, noEmit: true, jsx: 'preserve', strict: true, moduleDetection: 'force' }, include: ['*'] }))
    for (const item of corpus) {
      for (const [name, code] of Object.entries(item.files ?? {})) writeFileSync(join(dir, name), code)
    }
    for (const [index, item] of corpus.entries()) {
      await t.test(item.name, () => {
        const filename = resolve(dir, `case${index}.${item.language}`)
        writeFileSync(filename, item.code)
        const linter = new Linter({ cwd: dir })
        const messages = linter.verify(item.code, [{ files: ['**/*.{ts,js,jsx}'], languageOptions: { parser, parserOptions: { projectService: true, tsconfigRootDir: dir } }, ...plugin.configs.recommended }], { filename })
        assert.deepEqual(messages.map(m => m.message), item.expect.map(e => `use ${e.set}.${e.member} instead of '${e.literal}'`))
      })
    }
  } finally { rmSync(dir, { recursive: true, force: true }) }
})
