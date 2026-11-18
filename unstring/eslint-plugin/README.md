# eslint-plugin-unstring

Type-aware checks for named closed-set wire values. Requires ESLint 9+, TypeScript
5.7+, and a compatible `@typescript-eslint/parser`. The parser's TypeScript
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
`string`, `unknown`, `any`, and types without a matching const table. TypeScript
can erase alias identity during structural type canonicalization;
only types whose checker result retains their alias are eligible.

## Fixture contract

`../internal/typed/testdata/typescript.yaml` is the shared conformance corpus.
Each case has a unique `name`, `language` (`ts`, `js`, or `jsx`), source `code`,
and an ordered `expect` list of `{set, member, literal}` findings; an empty list
requires silence. The Go typed pass can consume the same cases when it supports
the corresponding languages. Required case names are protected by the test
manifest; add cases rather than replacing existing coverage.
