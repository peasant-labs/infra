import { VALUE_SYMBOL, CONST_NODE, ALIAS_SYMBOL, sameUnion } from './compiler.js'

function namedSet(checker, type, receiverSymbol) {
  if (type.aliasSymbol?.declarations?.length) return type.aliasSymbol

  // Preserve the written type reference, not a structurally equal union found
  // elsewhere. This also works for JSDoc @property and @param type nodes.
  for (const declaration of receiverSymbol?.declarations ?? []) {
    const nodes = [declaration.type, declaration.typeExpression?.type]
    // In JavaScript, a parameter's annotation lives on its function's JSDoc,
    // not on the parameter node itself.
    if (declaration.name?.getText() === receiverSymbol.getName()) {
      for (const doc of declaration.parent?.jsDoc ?? []) {
        for (const tag of doc.tags ?? []) {
          if (tag.tagName?.getText() === 'param' && tag.name?.getText() === receiverSymbol.getName()) {
            nodes.push(tag.typeExpression?.type)
          }
        }
      }
    }
    for (const node of nodes) {
      const name = node?.typeName ?? node?.qualifier
      if (!name) continue
      let symbol = checker.getSymbolAtLocation(name)
      if (symbol?.flags & ALIAS_SYMBOL) symbol = checker.getAliasedSymbol(symbol)
      if (!symbol?.declarations?.length) continue
      const declared = checker.getDeclaredTypeOfSymbol(symbol)
      if (declared.isUnion() && declared.types.every(part => part.isStringLiteral()) &&
          sameUnion(checker, declared, type)) {
        return symbol
      }
    }
  }
  return null
}

function memberFor(checker, receiver, literal) {
  let type = checker.getTypeAtLocation(receiver)
  const receiverSymbol = checker.getSymbolAtLocation(receiver) ??
    checker.getSymbolAtLocation(receiver.name ?? receiver.argumentExpression ?? receiver)
  // Synthetic property symbols (for example mapped or inferred types) can
  // have declarations without a valueDeclaration. Query at that declaration
  // to recover the complete declared type, not its control-flow narrowing.
  const declaration = receiverSymbol?.valueDeclaration ?? receiverSymbol?.declarations?.[0]
  if (declaration) {
    type = checker.getTypeOfSymbolAtLocation(receiverSymbol, declaration)
  }
  type = checker.getNonNullableType(type)
  if (!type.isUnion() || !type.types.every(part => part.isStringLiteral())) return null
  const alias = namedSet(checker, type, receiverSymbol)
  const aliasDeclaration = alias?.declarations?.[0]
  if (!aliasDeclaration) return null
  const name = alias.getName()
  const value = checker.resolveName(name, aliasDeclaration, VALUE_SYMBOL, false)
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
