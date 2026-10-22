// Copyright (c) 2025 Reliant Labs

/**
 * A panel formula ("A / B * 100") parsed the way forge parses it
 * (pkg/dashspec/formula.go) and turned into ClickHouse SQL with the same
 * arithmetic the dashboard applies: a division by zero is 0, and so is any
 * other result that is not a finite number.
 *
 *   expr    := term (('+' | '-') term)*
 *   term    := unary (('*' | '/') unary)*
 *   unary   := '-' unary | primary
 *   primary := number | ref | '(' expr ')'
 *
 * A ref is one uppercase letter, A for the first series up to J for the tenth.
 */

export const MAX_FORMULA_LEN = 256;
const MAX_DEPTH = 32;

export type FormulaNode =
  | { t: "num"; text: string }
  | { t: "ref"; index: number }
  | { t: "neg"; x: FormulaNode }
  | { t: "bin"; op: "+" | "-" | "*" | "/"; l: FormulaNode; r: FormulaNode };

const NUMBER = /^(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?/;

/** The formula's tree, or an Error saying what is wrong with it. */
export function parseFormula(source: string, seriesCount: number): FormulaNode {
  if (source.length > MAX_FORMULA_LEN) throw new Error(`the formula is longer than ${MAX_FORMULA_LEN} characters`);
  let pos = 0;
  let refs = 0;
  const skip = () => {
    while (pos < source.length && /\s/.test(source[pos])) pos++;
  };
  const fail = (what: string): never => {
    throw new Error(`${what} at position ${pos + 1}`);
  };
  const expr = (depth: number): FormulaNode => {
    if (depth > MAX_DEPTH) fail("the formula is nested too deeply");
    let left = term(depth);
    for (;;) {
      skip();
      const op = source[pos];
      if (op !== "+" && op !== "-") return left;
      pos++;
      left = { t: "bin", op, l: left, r: term(depth) };
    }
  };
  const term = (depth: number): FormulaNode => {
    let left = unary(depth);
    for (;;) {
      skip();
      const op = source[pos];
      if (op !== "*" && op !== "/") return left;
      pos++;
      left = { t: "bin", op, l: left, r: unary(depth) };
    }
  };
  const unary = (depth: number): FormulaNode => {
    skip();
    if (source[pos] === "-") {
      pos++;
      return { t: "neg", x: unary(depth + 1) };
    }
    return primary(depth);
  };
  const primary = (depth: number): FormulaNode => {
    skip();
    const c = source[pos];
    if (c === undefined) return fail("the formula ends too early");
    if (c === "(") {
      pos++;
      const inner = expr(depth + 1);
      skip();
      if (source[pos] !== ")") fail("expected ')'");
      pos++;
      return inner;
    }
    const num = NUMBER.exec(source.slice(pos));
    if (num) {
      pos += num[0].length;
      return { t: "num", text: num[0] };
    }
    if (c >= "A" && c <= "Z") {
      const index = c.charCodeAt(0) - 65;
      if (index >= seriesCount) fail(`"${c}" names a series the panel does not have (it has ${seriesCount})`);
      pos++;
      refs++;
      return { t: "ref", index };
    }
    return fail(`unexpected "${c}"`);
  };
  const tree = expr(0);
  skip();
  if (pos < source.length) fail(`unexpected "${source[pos]}"`);
  if (refs === 0) throw new Error("the formula uses no series");
  return tree;
}

/**
 * The tree as SQL. `ref(i)` is series i's aggregate and must be a single
 * expression (a call or a name), never a bare operator expression. Each
 * division and the whole result go through ifNotFinite(…, 0), so `1 + A / B`
 * is 1 when B is 0, as it is on the dashboard.
 */
export function formulaSql(tree: FormulaNode, ref: (index: number) => string): string {
  interface Sql {
    text: string;
    /** A literal, a call or a name: safe as an operand without parentheses. */
    atomic: boolean;
  }
  const operand = (x: Sql) => (x.atomic ? x.text : `(${x.text})`);
  const go = (n: FormulaNode): Sql => {
    switch (n.t) {
      case "num":
        return { text: n.text, atomic: true };
      case "ref":
        return { text: ref(n.index), atomic: true };
      case "neg":
        return { text: `-${operand(go(n.x))}`, atomic: false };
      case "bin": {
        const l = go(n.l);
        const r = go(n.r);
        if (n.op === "/") return { text: `ifNotFinite(${l.text} / ${operand(r)}, 0)`, atomic: true };
        return { text: `${operand(l)} ${n.op} ${operand(r)}`, atomic: false };
      }
    }
  };
  return `ifNotFinite(${go(tree).text}, 0)`;
}
