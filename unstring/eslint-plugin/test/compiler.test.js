import { test } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { parse } from 'yaml'
import { ALIAS_SYMBOL, CONST_NODE, VALUE_SYMBOL, sameUnion } from '../src/compiler.js'

test('compiler constants match the installed TypeScript', () => {
  assert.equal(ALIAS_SYMBOL, ts.SymbolFlags.Alias)
  assert.equal(CONST_NODE, ts.NodeFlags.Const)
  assert.equal(VALUE_SYMBOL, ts.SymbolFlags.Value)
})

test('union identity requires assignability in both directions', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'unstring-types-'))
  const fixtureDir = process.env.UNSTRING_FIXTURE_DIR ?? fileURLToPath(new URL('../../internal/typed/testdata/', import.meta.url))
  const corpus = parse(readFileSync(join(fixtureDir, 'assignability.yaml'), 'utf8'))
  try {
    const names = corpus.map(item => item.name)
    assert.equal(new Set(names).size, names.length)
    for (const name of ['same union', 'left is subset', 'left is superset']) assert.ok(names.includes(name), `missing fixture: ${name}`)
    for (const [index, item] of corpus.entries()) {
      await t.test(item.name, () => {
        const filename = join(dir, `case${index}.ts`)
        writeFileSync(filename, item.code)
        const program = ts.createProgram([filename], { strict: true, noEmit: true })
        const checker = program.getTypeChecker()
        const source = program.getSourceFile(filename)
        const types = new Map(source.statements.filter(ts.isTypeAliasDeclaration).map(node => [node.name.text, checker.getTypeAtLocation(node.name)]))
        assert.equal(sameUnion(checker, types.get('Left'), types.get('Right')), item.equal)
      })
    }
  } finally { rmSync(dir, { recursive: true, force: true }) }
})
