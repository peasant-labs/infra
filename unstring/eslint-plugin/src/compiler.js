// Compiler enum values are checked against the development TypeScript version
// in tests; the plugin does not load its own copy of the consumer's compiler.
export const VALUE_SYMBOL = 111551
export const CONST_NODE = 2
export const ALIAS_SYMBOL = 2097152

export function sameUnion(checker, left, right) {
  return checker.isTypeAssignableTo(left, right) && checker.isTypeAssignableTo(right, left)
}
