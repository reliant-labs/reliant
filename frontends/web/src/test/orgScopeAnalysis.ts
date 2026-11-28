// Copyright (c) 2025 Reliant Labs

/**
 * A static map of every org-scoped RPC read in `src/`, for the guard in
 * `__tests__/orgScopedQueries.guard.test.ts`.
 *
 * "Org-scoped" is derived, never listed: an rpc is org-scoped when its request
 * message has an `org_id` field in the generated `_pb.ts` (the caller passes
 * the descriptors in). Those are exactly the requests `orgInterceptor` fills
 * from the active org, so their answer depends on which org the tab is in.
 *
 * Three kinds of site are found, by syntax alone (no type checker, so the
 * guard stays in the fast tier):
 *
 *   - `generated`: a generated read, `use<Method>(req, opts)`,
 *     `use<Method>Infinite(req, opts)` or `<svc>Queries.<method>(req, opts)`
 *     from a `*-service-hooks_gen` module. Its key is protojson(req), so the
 *     key names the org exactly when the request does.
 *   - `handwritten`: an options object with `queryKey` and `queryFn` whose
 *     queryFn reaches an org-scoped rpc, directly or through the functions it
 *     calls (followed across imports, a few levels deep).
 *   - `call`: a direct client call of an org-scoped rpc anywhere (an
 *     imperative read or a write).
 *
 * Whether an expression "names the org" is a lexical test (an org identifier
 * such as `orgId`, `org`, `orgScope`, `orgRequest`, in the expression or in
 * the declarations it refers to). It is a guard, not a proof: it catches the
 * regression that matters (a key or request that says nothing about the org)
 * and the guard's allowlist records each deliberate exception with its reason.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import ts from "typescript";

/** The slice of a protobuf-es service descriptor this needs. */
export interface ServiceDesc {
  kind: "service";
  typeName: string;
  methods: { localName: string; methodKind: string; input: { fields: { name: string }[] } }[];
}

export interface Site {
  kind: "generated" | "handwritten" | "call";
  /** `path:line`, path relative to src/. */
  where: string;
  /** The function the site is in (`useProjects`), for an allowlist that survives line moves. */
  fn: string;
  /** `<service typeName>.<method localName>` of every org-scoped rpc the site reads or calls. */
  rpcs: string[];
  /** The key (handwritten) or request (generated, call) names the org. */
  namesOrg: boolean;
  /** Handwritten only: the queryFn names the org too, so the request sent is the one the key describes. */
  fetchNamesOrg: boolean;
  /** `enabled` is present and names the org (or its readiness). Queries only. */
  gated: boolean;
  /** `placeholderData: keepPreviousData` (not org-aware). Queries only. */
  keepsAcrossOrgs: boolean;
  /** The call opts out of the active org on purpose (`acrossAllOrgs`). Calls only. */
  acrossAllOrgs: boolean;
}

// `scopedClient(Service, connectClient(Service))` is the generated hooks'
// per-call client (forge-web-runtime/transport-scope): its first argument is
// the service too.
const CLIENT_FACTORIES = new Set(["connectClient", "getControlPlaneClient", "createClient", "createPromiseClient", "scopedClient"]);
const CLIENT_FACTORY_CALL = /\b(?:connectClient|getControlPlaneClient|createClient|createPromiseClient|scopedClient)\s*[(<]/;

const IMPERATIVE_READS = new Set(["fetchQuery", "ensureQueryData", "prefetchQuery", "fetchInfiniteQuery", "prefetchInfiniteQuery", "ensureInfiniteQueryData"]);

const QUERY_HOOK = /^(createQueryHook|createInfiniteQueryHook|createSuspenseQueryHook)$/;

/** The rpc a `<svc>Queries` member reads: `listMembersInfinite` is ListMembers, paged. */
const rpcOfQueriesMember = (member: string) => member.replace(/Infinite$/, "");

/**
 * An identifier that says which org: `org`, `orgId`, `activeOrgId`,
 * `orgScope`, `getOrgScope()`, `orgRequest(…)`, `orgQueryEnabled(…)`,
 * `keepPreviousInOrg(…)`. Identifiers only: a string such as "org-envs" in a
 * key names a family, not an org.
 */
const ORG_IDENT =
  /^(?:org|orgId|orgID|orgScope|getOrgScope|useOrgScope|orgRequest|orgQueryEnabled|withOrgFailure|whenOrgScopeReady|keepPreviousInOrg|keepPreviousInOrgSegment|organizationId)$|OrgId$|OrgID$/;

function hasOrgIdentifier(node: ts.Node): boolean {
  let found = false;
  const visit = (n: ts.Node) => {
    if (found) return;
    if (ts.isIdentifier(n) && ORG_IDENT.test(n.text)) found = true;
    else ts.forEachChild(n, visit);
  };
  visit(node);
  return found;
}

export function orgScopedRpcs(services: ServiceDesc[]): Set<string> {
  const out = new Set<string>();
  for (const svc of services) {
    for (const m of svc.methods) {
      if (m.input.fields.some((f) => f.name === "org_id")) out.add(`${svc.typeName}.${m.localName}`);
    }
  }
  return out;
}

interface FileInfo {
  path: string;
  sf: ts.SourceFile;
  /** local name -> where it comes from */
  imports: Map<string, { from: string | null; name: string }>;
  /** top-level declarations by name */
  decls: Map<string, ts.Node>;
  /** `export { a as b } from "./x"` and `export * from "./x"` */
  reexports: Map<string, { from: string; name: string }>;
  starExports: string[];
}

interface Resolved {
  node: ts.Node;
  file: FileInfo;
}

export class OrgScopeAnalysis {
  private files = new Map<string, FileInfo | null>();
  /** pb file (absolute, no extension) -> export name -> service */
  private pbServices = new Map<string, Map<string, ServiceDesc>>();
  private scoped: Set<string>;
  private reachMemo = new Map<ts.Node, Set<string>>();
  private namesMemo = new Map<ts.Node, boolean>();

  private srcDir: string;

  constructor(srcDir: string, pbModules: Record<string, Record<string, unknown>>) {
    this.srcDir = srcDir;
    const all: ServiceDesc[] = [];
    for (const [path, mod] of Object.entries(pbModules)) {
      const byName = new Map<string, ServiceDesc>();
      for (const [name, value] of Object.entries(mod)) {
        if (value && typeof value === "object" && (value as { kind?: unknown }).kind === "service") {
          byName.set(name, value as ServiceDesc);
          all.push(value as ServiceDesc);
        }
      }
      this.pbServices.set(stripExt(path), byName);
    }
    this.scoped = orgScopedRpcs(all);
    this.scopedNames = new Set([...this.scoped].map((key) => key.slice(key.lastIndexOf(".") + 1)));
  }

  /** The method names of the org-scoped rpcs (`listDashboards`). */
  private scopedNames: Set<string>;

  get orgScoped(): ReadonlySet<string> {
    return this.scoped;
  }

  /** Every site in the source tree (tests, generated code and mocks excluded). */
  sites(): Site[] {
    const out: Site[] = [];
    const relevant: string[] = [];
    for (const path of listSources(this.srcDir)) {
      // Only a file that reads (an options object, a generated hook) or calls a
      // client can hold a site; the files those reach are parsed on demand.
      const text = readFileSync(path, "utf8");
      const calls = CLIENT_FACTORY_CALL.test(text);
      if (calls) this.clientFiles.push(path);
      if (calls || text.includes("queryFn") || text.includes("hooks_gen")) relevant.push(path);
    }
    for (const path of relevant) {
      const file = this.file(path);
      if (file) this.visitFile(file, out);
    }
    return out;
  }

  /** Files that construct a client: where every function that reaches an rpc ends. */
  private clientFiles: string[] = [];
  private byNameMemo: Map<string, Resolved[]> | null = null;

  /**
   * Top-level functions named `name` in the files that construct a client.
   * For a call through an object nothing here can resolve (an api from a React
   * context: `useObservabilityApi().listDashboards(…)`), when the method has
   * an org-scoped rpc's name.
   */
  private functionsNamed(name: string): Resolved[] {
    if (!this.byNameMemo) {
      this.byNameMemo = new Map();
      for (const path of this.clientFiles) {
        const file = this.file(path);
        if (!file) continue;
        for (const [n, node] of file.decls) {
          if (!isFunctionish(node)) continue;
          const list = this.byNameMemo.get(n) ?? [];
          list.push({ node, file });
          this.byNameMemo.set(n, list);
        }
      }
    }
    return this.byNameMemo.get(name) ?? [];
  }

  // ── files and declarations ────────────────────────────────────────────────

  private file(path: string): FileInfo | null {
    if (this.files.has(path)) return this.files.get(path)!;
    let info: FileInfo | null = null;
    try {
      const text = readFileSync(path, "utf8");
      const sf = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true, path.endsWith("x") ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
      info = { path, sf, imports: new Map(), decls: new Map(), reexports: new Map(), starExports: [] };
      for (const st of sf.statements) this.indexStatement(info, st);
    } catch {
      info = null;
    }
    this.files.set(path, info);
    return info;
  }

  private indexStatement(info: FileInfo, st: ts.Statement): void {
    if (ts.isImportDeclaration(st) && ts.isStringLiteral(st.moduleSpecifier)) {
      const from = this.resolveModule(info.path, st.moduleSpecifier.text);
      const clause = st.importClause;
      if (clause?.name) info.imports.set(clause.name.text, { from, name: "default" });
      const nb = clause?.namedBindings;
      if (nb && ts.isNamedImports(nb)) {
        for (const el of nb.elements) info.imports.set(el.name.text, { from, name: (el.propertyName ?? el.name).text });
      }
      if (nb && ts.isNamespaceImport(nb)) info.imports.set(nb.name.text, { from, name: "*" });
    } else if (ts.isExportDeclaration(st) && st.moduleSpecifier && ts.isStringLiteral(st.moduleSpecifier)) {
      const from = this.resolveModule(info.path, st.moduleSpecifier.text);
      if (!from) return;
      if (!st.exportClause) info.starExports.push(from);
      else if (ts.isNamedExports(st.exportClause)) {
        for (const el of st.exportClause.elements) info.reexports.set(el.name.text, { from, name: (el.propertyName ?? el.name).text });
      }
    } else if (ts.isFunctionDeclaration(st) && st.name) {
      info.decls.set(st.name.text, st);
    } else if (ts.isVariableStatement(st)) {
      for (const d of st.declarationList.declarations) if (ts.isIdentifier(d.name)) info.decls.set(d.name.text, d);
    } else if (ts.isClassDeclaration(st) && st.name) {
      info.decls.set(st.name.text, st);
    }
  }

  private moduleMemo = new Map<string, string | null>();

  private resolveModule(fromFile: string, spec: string): string | null {
    const memoKey = spec.startsWith(".") ? `${dirname(fromFile)}\0${spec}` : spec;
    if (this.moduleMemo.has(memoKey)) return this.moduleMemo.get(memoKey)!;
    const resolved = this.resolveModuleUncached(fromFile, spec);
    this.moduleMemo.set(memoKey, resolved);
    return resolved;
  }

  private resolveModuleUncached(fromFile: string, spec: string): string | null {
    let base: string;
    if (spec.startsWith("@/")) base = join(this.srcDir, spec.slice(2));
    else if (spec.startsWith(".")) base = join(dirname(fromFile), spec);
    else return null;
    for (const cand of [base, `${base}.ts`, `${base}.tsx`, join(base, "index.ts"), join(base, "index.tsx")]) {
      try {
        if (statSync(cand).isFile()) return cand;
      } catch {
        // next candidate
      }
    }
    return null;
  }

  /** The declaration `name` exports from `path`, following re-exports. */
  private exported(path: string, name: string, depth = 0): Resolved | null {
    if (depth > 6) return null;
    const file = this.file(path);
    if (!file) return null;
    const own = file.decls.get(name);
    if (own) return { node: own, file };
    const imp = file.imports.get(name);
    const re = file.reexports.get(name);
    if (re) return this.exported(re.from, re.name, depth + 1);
    // `import { x } from "./a"; export { x };`
    if (imp?.from) return this.exported(imp.from, imp.name, depth + 1);
    for (const star of file.starExports) {
      const hit = this.exported(star, name, depth + 1);
      if (hit) return hit;
    }
    return null;
  }

  /** What an identifier refers to: a local declaration, a top-level one, or an import. */
  private lookup(id: ts.Identifier, file: FileInfo): Resolved | null {
    const name = id.text;
    for (let n: ts.Node | undefined = id.parent; n; n = n.parent) {
      const statements = ts.isBlock(n) || ts.isSourceFile(n) || ts.isModuleBlock(n) ? n.statements : null;
      if (statements) {
        for (const st of statements) {
          if (ts.isVariableStatement(st)) {
            for (const d of st.declarationList.declarations) if (ts.isIdentifier(d.name) && d.name.text === name) return { node: d, file };
          } else if (ts.isFunctionDeclaration(st) && st.name?.text === name) return { node: st, file };
        }
      }
      if (ts.isFunctionLike(n)) {
        for (const p of n.parameters) if (ts.isIdentifier(p.name) && p.name.text === name) return { node: p, file };
      }
    }
    const imp = file.imports.get(name);
    if (imp?.from) return imp.name === "*" ? null : this.exported(imp.from, imp.name);
    return null;
  }

  // ── services and clients ─────────────────────────────────────────────────

  /** The service an identifier names, when it is imported from a `_pb` module. */
  private serviceOf(expr: ts.Expression, file: FileInfo): ServiceDesc | null {
    if (!ts.isIdentifier(expr)) return null;
    const imp = file.imports.get(expr.text);
    if (imp?.from) return this.pbServices.get(stripExt(imp.from))?.get(imp.name) ?? null;
    const own = this.pbServices.get(stripExt(file.path))?.get(expr.text);
    return own ?? null;
  }

  /** The service whose client `expr` evaluates to, if it can be told. */
  private clientOf(expr: ts.Expression, file: FileInfo, depth = 0): ServiceDesc | null {
    if (depth > 5) return null;
    expr = unwrap(expr);
    if (ts.isCallExpression(expr)) {
      const callee = unwrap(expr.expression);
      if (ts.isIdentifier(callee) && CLIENT_FACTORIES.has(callee.text) && expr.arguments[0]) {
        return this.serviceOf(unwrap(expr.arguments[0]), file);
      }
      const fn = ts.isIdentifier(callee) ? this.lookup(callee, file) : null;
      if (fn) {
        for (const ret of returnedExpressions(fn.node)) {
          const svc = this.clientOf(ret, fn.file, depth + 1);
          if (svc) return svc;
        }
      }
      return null;
    }
    if (ts.isIdentifier(expr)) {
      const d = this.lookup(expr, file);
      if (d && ts.isVariableDeclaration(d.node) && d.node.initializer) return this.clientOf(d.node.initializer, d.file, depth + 1);
    }
    return null;
  }

  /** `rpc` keys of the org-scoped rpcs a client member call makes, or null when it is not one. */
  private rpcOfCall(call: ts.CallExpression, file: FileInfo): string | null {
    const callee = unwrap(call.expression);
    if (!ts.isPropertyAccessExpression(callee)) return null;
    const svc = this.clientOf(callee.expression, file);
    if (!svc) return null;
    const key = `${svc.typeName}.${callee.name.text}`;
    return this.scoped.has(key) ? key : null;
  }

  // ── generated hooks ──────────────────────────────────────────────────────

  /** For a generated hooks module: hook/queries export name -> rpc (or queries object -> service). */
  private generatedTable = new Map<string, { hooks: Map<string, string>; queries: Map<string, ServiceDesc> } | null>();

  private generated(path: string): { hooks: Map<string, string>; queries: Map<string, ServiceDesc> } | null {
    if (this.generatedTable.has(path)) return this.generatedTable.get(path)!;
    let table: { hooks: Map<string, string>; queries: Map<string, ServiceDesc> } | null = null;
    const file = path.endsWith("-hooks_gen.ts") ? this.file(path) : null;
    if (file) {
      table = { hooks: new Map(), queries: new Map() };
      // `const clientOf = scopedClient(ProjectService, connectClient(ProjectService));`,
      // or, from a forge before #2737, `const client = connectClient(ProjectService);`
      const clientDecl = file.decls.get("clientOf") ?? file.decls.get("client");
      const svc = clientDecl && ts.isVariableDeclaration(clientDecl) && clientDecl.initializer ? this.clientOf(clientDecl.initializer, file) : null;
      if (svc) {
        for (const [name, decl] of file.decls) {
          if (!ts.isVariableDeclaration(decl) || !decl.initializer) continue;
          const init = decl.initializer;
          if (name.endsWith("Queries") && ts.isObjectLiteralExpression(init)) table.queries.set(name, svc);
          // `export const useListProjects = createQueryHook<…>(projectServiceQueries.listProjects, …)`
          if (ts.isCallExpression(init) && ts.isIdentifier(init.expression) && QUERY_HOOK.test(init.expression.text)) {
            const arg = init.arguments[0];
            // A paged read (`…Queries.listMembersInfinite`) is a read of its rpc.
            if (arg && ts.isPropertyAccessExpression(arg)) table.hooks.set(name, `${svc.typeName}.${rpcOfQueriesMember(arg.name.text)}`);
          }
        }
      }
    }
    this.generatedTable.set(path, table);
    return table;
  }

  /** The rpc a generated read call is for: `useX(req)` or `xServiceQueries.x(req)`. */
  private generatedRead(call: ts.CallExpression, file: FileInfo): string | null {
    const callee = unwrap(call.expression);
    if (ts.isIdentifier(callee)) {
      const d = this.lookup(callee, file);
      if (!d || !d.file.path.endsWith("-hooks_gen.ts")) return null;
      return this.generated(d.file.path)?.hooks.get(nameOf(d.node) ?? "") ?? null;
    }
    if (ts.isPropertyAccessExpression(callee) && ts.isIdentifier(callee.expression)) {
      const d = this.lookup(callee.expression, file);
      if (!d || !d.file.path.endsWith("-hooks_gen.ts")) return null;
      const svc = this.generated(d.file.path)?.queries.get(nameOf(d.node) ?? "");
      return svc ? `${svc.typeName}.${rpcOfQueriesMember(callee.name.text)}` : null;
    }
    return null;
  }

  // ── reachability ─────────────────────────────────────────────────────────

  /** The org-scoped rpcs `node` reaches: client calls in it, and in the functions it calls. */
  private reaches(node: ts.Node, file: FileInfo): Set<string> {
    const memo = this.reachMemo.get(node);
    if (memo) return memo;
    const out = new Set<string>();
    this.reachMemo.set(node, out); // cycle guard
    const visit = (n: ts.Node) => {
      if (ts.isCallExpression(n)) {
        const rpc = this.rpcOfCall(n, file) ?? this.generatedRead(n, file);
        if (rpc && this.scoped.has(rpc)) out.add(rpc);
        const callee = unwrap(n.expression);
        const target = ts.isIdentifier(callee)
          ? this.lookup(callee, file)
          : ts.isPropertyAccessExpression(callee)
            ? this.memberTarget(callee, file)
            : null;
        if (target && isFunctionish(target.node)) {
          for (const r of this.reaches(target.node, target.file)) out.add(r);
        } else if (
          !target &&
          ts.isPropertyAccessExpression(callee) &&
          this.scopedNames.has(callee.name.text) &&
          !this.clientOf(callee.expression, file)
        ) {
          const named = this.functionsNamed(callee.name.text);
          for (const fn of named) {
            for (const r of this.reaches(fn.node, fn.file)) out.add(r);
          }
          // No function by that name: an api object that wraps the client
          // method by method (`api.listReleases(…)`). Its name is the rpc's.
          if (named.length === 0) {
            for (const key of this.scoped) if (key.endsWith(`.${callee.name.text}`)) out.add(key);
          }
        }
      }
      ts.forEachChild(n, visit);
    };
    ts.forEachChild(node, visit);
    return out;
  }

  /** `obj.fn` where obj is a const object literal (or a namespace import) declaring fn. */
  private memberTarget(access: ts.PropertyAccessExpression, file: FileInfo): Resolved | null {
    const recv = unwrap(access.expression);
    if (!ts.isIdentifier(recv)) return null;
    const imp = file.imports.get(recv.text);
    if (imp?.from && imp.name === "*") return this.exported(imp.from, access.name.text);
    const d = this.lookup(recv, file);
    if (d && ts.isVariableDeclaration(d.node) && d.node.initializer && ts.isObjectLiteralExpression(unwrap(d.node.initializer))) {
      const obj = unwrap(d.node.initializer) as ts.ObjectLiteralExpression;
      for (const p of obj.properties) {
        if (p.name && ts.isIdentifier(p.name) && p.name.text === access.name.text) {
          if (ts.isMethodDeclaration(p)) return { node: p, file: d.file };
          if (ts.isPropertyAssignment(p)) return { node: p.initializer, file: d.file };
        }
      }
    }
    return null;
  }

  // ── "names the org" ──────────────────────────────────────────────────────

  /**
   * Whether `expr` says which org: an org identifier in it, in the value of a
   * const it names, or in the body of a function it calls (`projectsKey()`,
   * whose default argument is the tab's org). Two levels deep.
   */
  private namesOrg(expr: ts.Node, file: FileInfo, depth = 0): boolean {
    if (hasOrgIdentifier(expr)) return true;
    if (depth >= 2) return false;
    const memo = this.namesMemo.get(expr);
    if (memo !== undefined) return memo;
    this.namesMemo.set(expr, false);
    let found = false;
    const visit = (n: ts.Node) => {
      if (found) return;
      if (ts.isCallExpression(n)) {
        const callee = unwrap(n.expression);
        const fn = ts.isIdentifier(callee) ? this.lookup(callee, file) : ts.isPropertyAccessExpression(callee) ? this.memberTarget(callee, file) : null;
        if (fn && isFunctionish(fn.node)) {
          if (this.namesOrg(fn.node, fn.file, depth + 1)) found = true;
        } else if (ts.isPropertyAccessExpression(callee)) {
          // `api.getRelease(…)` on an api nothing here can resolve: what the
          // receiver was made from (`useSomeApi(org.orgId)`) says.
          visit(callee.expression);
        }
        // The arguments still count; a resolved callee was judged by its body alone.
        for (const arg of n.arguments) visit(arg);
        return;
      }
      if (ts.isPropertyAccessExpression(n)) {
        // `KEYS.wallet`: that one member's value, never the whole object.
        const member = this.memberTarget(n, file);
        if (member && this.namesOrg(member.node, member.file, depth + 1)) found = true;
        if (!member) visit(n.expression);
        return;
      }
      if (ts.isIdentifier(n)) {
        const d = this.lookup(n, file);
        if (d && ts.isVariableDeclaration(d.node) && d.node.initializer && this.namesOrg(d.node.initializer, d.file, depth + 1)) found = true;
      }
      ts.forEachChild(n, visit);
    };
    visit(expr);
    this.namesMemo.set(expr, found);
    return found;
  }

  // ── sites ────────────────────────────────────────────────────────────────

  private where(node: ts.Node, file: FileInfo): string {
    const { line } = file.sf.getLineAndCharacterOfPosition(node.getStart(file.sf));
    return `${relative(this.srcDir, file.path)}:${line + 1}`;
  }

  private visitFile(file: FileInfo, out: Site[]): void {
    const visit = (n: ts.Node) => {
      if (ts.isCallExpression(n)) {
        const gen = this.generatedRead(n, file);
        // `<svc>Queries.<method>(req).queryKey` only derives a key: not a read.
        const keyOnly = ts.isPropertyAccessExpression(n.parent) && n.parent.name.text === "queryKey";
        if (gen && this.scoped.has(gen) && !keyOnly) {
          const req = n.arguments[0];
          const opts = n.arguments[1] && ts.isObjectLiteralExpression(unwrap(n.arguments[1])) ? (unwrap(n.arguments[1]) as ts.ObjectLiteralExpression) : null;
          out.push({
            kind: "generated",
            where: this.where(n, file),
            fn: enclosingName(n),
            rpcs: [gen],
            namesOrg: !!req && this.namesOrg(req, file),
            fetchNamesOrg: true,
            gated: this.gated(opts, file) || this.imperativeAfterOrg(n, file),
            keepsAcrossOrgs: keepsAcrossOrgs(opts),
            acrossAllOrgs: false,
          });
        }
        const rpc = this.rpcOfCall(n, file);
        if (rpc) {
          const req = n.arguments[0];
          out.push({
            kind: "call",
            where: this.where(n, file),
            fn: enclosingName(n),
            rpcs: [rpc],
            namesOrg: !!req && this.namesOrg(req, file),
            fetchNamesOrg: true,
            gated: false,
            keepsAcrossOrgs: false,
            acrossAllOrgs: !!n.arguments[1] && /acrossAllOrgs/.test(n.arguments[1].getText(file.sf)),
          });
        }
      }
      if (ts.isObjectLiteralExpression(n)) {
        const key = prop(n, "queryKey");
        const fn = prop(n, "queryFn");
        if (key && fn) {
          const rpcs = [...this.reaches(fn, file)];
          if (rpcs.length) {
            out.push({
              kind: "handwritten",
              where: this.where(n, file),
              fn: enclosingName(n),
              rpcs: rpcs.sort(),
              namesOrg: this.namesOrg(key, file),
              fetchNamesOrg: this.namesOrg(fn, file),
              gated: this.gated(n, file) || this.imperativeAfterOrg(n, file),
              keepsAcrossOrgs: keepsAcrossOrgs(n),
              acrossAllOrgs: false,
            });
          }
        }
      }
      ts.forEachChild(n, visit);
    };
    visit(file.sf);
  }

  /** `enabled` waits for the org (names it, or its readiness), or is `false`: a cache-only read that never fetches. */
  private gated(opts: ts.ObjectLiteralExpression | null, file: FileInfo): boolean {
    const enabled = opts ? prop(opts, "enabled") : null;
    if (!enabled) return false;
    if (enabled.kind === ts.SyntaxKind.FalseKeyword) return true;
    return this.namesOrg(enabled, file);
  }

  /**
   * An imperative read (`queryClient.fetchQuery(…)`, `ensureQueryData`,
   * `prefetchQuery`) has no `enabled`: it is gated when its function awaits
   * the org first (`whenOrgScopeReady`, as fetchProjects does).
   */
  private imperativeAfterOrg(site: ts.Node, file: FileInfo): boolean {
    let call: ts.Node = site;
    while (call.parent && !ts.isCallExpression(call.parent)) {
      if (ts.isFunctionLike(call.parent)) return false;
      call = call.parent;
    }
    const parent = call.parent;
    if (!parent || !ts.isCallExpression(parent)) return false;
    const callee = unwrap(parent.expression);
    const name = ts.isPropertyAccessExpression(callee) ? callee.name.text : ts.isIdentifier(callee) ? callee.text : "";
    if (!IMPERATIVE_READS.has(name)) return false;
    for (let n: ts.Node | undefined = parent; n; n = n.parent) {
      if (ts.isFunctionLike(n)) return /\bwhenOrgScopeReady\b/.test(n.getText(file.sf));
    }
    return false;
  }
}

// ── helpers ──────────────────────────────────────────────────────────────────

function stripExt(path: string): string {
  return path.replace(/\.tsx?$/, "");
}

function unwrap(e: ts.Expression): ts.Expression {
  for (;;) {
    if (ts.isParenthesizedExpression(e) || ts.isAsExpression(e) || ts.isNonNullExpression(e) || ts.isAwaitExpression(e) || ts.isSatisfiesExpression(e)) e = e.expression;
    else return e;
  }
}

/** The name of the nearest named function (or const) around `node`; "" at top level. */
function enclosingName(node: ts.Node): string {
  for (let n: ts.Node | undefined = node.parent; n; n = n.parent) {
    if ((ts.isFunctionDeclaration(n) || ts.isMethodDeclaration(n)) && n.name && ts.isIdentifier(n.name)) return n.name.text;
    if (ts.isVariableDeclaration(n) && ts.isIdentifier(n.name) && n.initializer && isFunctionish(n)) return n.name.text;
  }
  return "";
}

function nameOf(node: ts.Node): string | undefined {
  if ((ts.isVariableDeclaration(node) || ts.isFunctionDeclaration(node)) && node.name && ts.isIdentifier(node.name)) return node.name.text;
  return undefined;
}

function isFunctionish(node: ts.Node): boolean {
  if (ts.isFunctionDeclaration(node) || ts.isMethodDeclaration(node) || ts.isArrowFunction(node) || ts.isFunctionExpression(node)) return true;
  return ts.isVariableDeclaration(node) && !!node.initializer && (ts.isArrowFunction(unwrap(node.initializer)) || ts.isFunctionExpression(unwrap(node.initializer)));
}

/** What a function returns: an arrow's expression body, or its own `return`s. */
function returnedExpressions(node: ts.Node): ts.Expression[] {
  let fn: ts.Node = node;
  if (ts.isVariableDeclaration(node) && node.initializer) fn = unwrap(node.initializer);
  if (!(ts.isFunctionDeclaration(fn) || ts.isArrowFunction(fn) || ts.isFunctionExpression(fn) || ts.isMethodDeclaration(fn)) || !fn.body) return [];
  if (!ts.isBlock(fn.body)) return [fn.body];
  const out: ts.Expression[] = [];
  const visit = (n: ts.Node) => {
    if (ts.isReturnStatement(n) && n.expression) out.push(n.expression);
    if (!ts.isFunctionLike(n)) ts.forEachChild(n, visit);
  };
  ts.forEachChild(fn.body, visit);
  return out;
}

function prop(obj: ts.ObjectLiteralExpression, name: string): ts.Node | null {
  for (const p of obj.properties) {
    if (!p.name || !(ts.isIdentifier(p.name) || ts.isStringLiteral(p.name)) || p.name.text !== name) continue;
    if (ts.isPropertyAssignment(p)) return p.initializer;
    if (ts.isShorthandPropertyAssignment(p)) return p.name;
    if (ts.isMethodDeclaration(p)) return p;
  }
  return null;
}

function keepsAcrossOrgs(opts: ts.ObjectLiteralExpression | null): boolean {
  const ph = opts ? prop(opts, "placeholderData") : null;
  return !!ph && ts.isIdentifier(ph) && ph.text === "keepPreviousData";
}

function listSources(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name === "gen" || name === "__tests__" || name === "test" || name === "mocks" || name === "node_modules") continue;
      listSources(p, out);
    } else if (/\.tsx?$/.test(name) && !/\.(test|spec)\.tsx?$/.test(name) && !name.endsWith("_gen.ts") && !name.endsWith(".d.ts")) {
      out.push(p);
    }
  }
  return out;
}
