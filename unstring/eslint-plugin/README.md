# eslint-plugin-unstring

Type-aware checks for named closed-set wire values. Requires ESLint 9+, TypeScript
5.7 through 6.x, and a compatible `@typescript-eslint/parser`. The parser's TypeScript
program must include every linted file; for JavaScript enable `allowJs` and
`checkJs` in its tsconfig.

```js
// eslint.config.js
import parser from '@typescript-eslint/parser'
import unstring from 'eslint-plugin-unstring'

export default [
  {
    files: ['**/*.{ts,tsx,js,jsx}'],
    languageOptions: { parser, parserOptions: { projectService: true } },
    ...unstring.configs.recommended,
  },
]
```

`unstring/closed-set-literal` reports equality comparisons and switch cases when
the receiver resolves to a named string-literal union and a same-named `const`
table in the type declaration's scope has a member with that wire value. For
example, pair `type Role = 'user' | 'assistant'` with
`const Role = { User: 'user', Assistant: 'assistant' } as const`, then write
`role === Role.User`. Frozen JSDoc tables and members with a literal `wire`
property also work. The rule does not fix code; it ignores unnamed unions,
`string`, `unknown`, `any`, and types without a matching const table. When the
checker erases an alias, the rule also follows an explicit named type reference
on a property or parameter, including a JSDoc import. It never identifies a set
by matching union values alone. Generated declarations that inline a union
without a named reference, or widen each table member to the whole union, cannot
be resolved reliably and remain silent. Nullable and optional sets are checked
after removing `null` and `undefined`.

An `any` receiver is deliberately silent: no value-based guessing is performed.
For an untyped JavaScript fixture payload, add a JSDoc annotation to the helper
parameter, for example `@param {import('@peasant-labs/schema').MapPayload} payload`,
using the actual exported payload type. The property must then resolve to a
named set and a precise same-named const table.

### Identity resolution

1. Get the expression type, then, when its symbol has a declaration, obtain the
   type at that declaration (rather than a control-flow-narrowed type). Strip
   nullish members; refuse anything other than a union of string literals.
   `narrowed mapped property uses declared set` forces this lookup.
2. Use the resulting type's alias symbol when present. `jsx comparison` and
   `narrowed mapped property uses declared set` force this path.
3. Otherwise resolve a named type reference written on the symbol's declaration,
   including imports. The resolved union must be mutually assignable to the
   receiver's non-nullish type. `imported role property in jsdoc` forces this
   path; `left is subset` and `left is superset` protect both directions.
4. JavaScript parameter declarations do not carry their JSDoc type directly, so
   inspect the matching function `@param` tag for the same named-reference
   resolution. `imported jsdoc parameter` forces this path.

There is no fallback that identifies a set by matching its values or by a
structural `type.symbol`. Once identified, the same-named const table is
resolved in the alias declaration's scope and a literal member wire value is
required.

## Fixture contract

`../internal/typed/testdata/typescript.yaml` is the shared conformance corpus,
resolved relative to this package; `UNSTRING_FIXTURE_DIR` can override its
directory for tests. `assignability.yaml` covers the internal type equivalence
check.
Each case has a unique `name`, `language` (`ts`, `js`, or `jsx`), source `code`,
an optional `files` mapping of supporting filenames to source text, and an
ordered `expect` list of `{set, member, literal, line, column}` findings; an empty list
requires silence. The Go typed pass can consume the same cases when it supports
the corresponding languages. Required case names are protected by the test
manifest; add cases rather than replacing existing coverage.
