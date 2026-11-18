// SymbolFlags.Value: the compiler API accepts this stable bit mask without
// loading a second copy of the consumer's TypeScript compiler.
const VALUE_SYMBOL = 111551
const CONST_NODE = 2

function memberFor(checker, receiver, literal) {
  let type = checker.getTypeAtLocation(receiver)
  const receiverSymbol = checker.getSymbolAtLocation(receiver)
  if (receiverSymbol?.valueDeclaration) {
    type = checker.getTypeOfSymbolAtLocation(receiverSymbol, receiverSymbol.valueDeclaration)
  }
  if (!type.isUnion() || !type.types.every(part => part.isStringLiteral())) return null
  const alias = type.aliasSymbol
  const declaration = alias?.declarations?.[0]
  if (!declaration) return null
  const name = alias.getName()
  const value = checker.resolveName(name, declaration, VALUE_SYMBOL, false)
  const valueDecl = value?.valueDeclaration
  // A runtime table must be a const binding, not a coincidentally named function,
  // mutable variable, or ambient type. TypeScript NodeFlags.Const is bit 1.
  if (!valueDecl?.parent || !(valueDecl.parent.flags & CONST_NODE)) return null
  const table = checker.getTypeOfSymbolAtLocation(value, valueDecl)
  for (const member of checker.getPropertiesOfType(table)) {
    const memberType = checker.getTypeOfSymbolAtLocation(member, valueDecl)
    let wire = memberType
    const wireSymbol = memberType.getProperty('wire')
    if (wireSymbol) wire = checker.getTypeOfSymbolAtLocation(wireSymbol, valueDecl)
    if (wire.isStringLiteral() && wire.value === literal) {
      return { set: name, member: member.getName() }
    }
  }
  return null
}

export default {
  meta: {
    type: 'suggestion',
    docs: { description: 'Use named closed-set members instead of wire literals' },
    schema: [],
    messages: { member: "use {{set}}.{{member}} instead of '{{literal}}'" },
  },
  create(context) {
    const services = context.sourceCode.parserServices
    if (!services?.program || !services.esTreeNodeToTSNodeMap) {
      throw new Error('unstring/closed-set-literal requires type information; configure @typescript-eslint/parser with parserOptions.projectService')
    }
    const checker = services.program.getTypeChecker()
    function check(receiver, literal) {
      if (literal?.type !== 'Literal' || typeof literal.value !== 'string') return
      const result = memberFor(checker, services.esTreeNodeToTSNodeMap.get(receiver), literal.value)
      if (result) context.report({ node: literal, messageId: 'member', data: { ...result, literal: literal.value } })
    }
    return {
      BinaryExpression(node) {
        if (!['===', '!==', '==', '!='].includes(node.operator)) return
        check(node.left, node.right)
        check(node.right, node.left)
      },
      SwitchCase(node) {
        if (node.test) check(node.parent.discriminant, node.test)
      },
    }
  },
}
