const assert = require("node:assert/strict");
const { execFileSync } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");

// Run the real script; replace only the expensive external scaffold/build tools.
// Capture the authorization input AT generate time, not just the final file.
// Run: node --test examples/nextjs-static-go-api/scaffold.test.cjs
test("notes scaffold explicitly makes every CRUD RPC anonymous before regeneration", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "notes-scaffold-"));
  try {
    const bin = path.join(root, "bin");
    fs.mkdirSync(bin);
    fs.writeFileSync(
      path.join(bin, "fixture-forge"),
      `#!/usr/bin/env node
const fs = require("node:fs");
const path = require("node:path");
const args = process.argv.slice(2);
const proto = "proto/services/notes/v1/notes.proto";
if (args[0] === "project" && args[1] === "new") {
  if (!args.includes("none") || !args.includes("--claimable")) process.exit(1);
  fs.mkdirSync(path.join(args[2], path.dirname(proto)), {recursive: true});
  fs.mkdirSync(path.join(args[2], "frontends/web"), {recursive: true});
  fs.writeFileSync(path.join(args[2], proto), 'syntax = "proto3";\\n');
} else if (args[0] === "scaffold") {
  const rpcs = ["CreateNote", "GetNote", "UpdateNote", "DeleteNote", "ListNotes"];
  fs.appendFileSync(proto, "\\nservice NotesService {\\n" + rpcs.map(name =>
    "rpc " + name + " (Request) returns (Response) {\\n" +
    "  option (forge.options.api) = { auth_required: true };\\n}\\n"
  ).join("") + "}\\n");
} else if (args[0] === "generate") {
  fs.copyFileSync(proto, "generate-input.proto");
} else {
  process.exit(1);
}
`,
      { mode: 0o755 },
    );
    for (const tool of ["go", "npm"]) {
      fs.writeFileSync(
        path.join(bin, tool),
        '#!/bin/sh\n[ "$1" = "build" ] || [ "$1 $2" = "run build" ]\n',
        { mode: 0o755 },
      );
    }
    execFileSync("bash", [path.join(__dirname, "scaffold.sh"), "notes"], {
      cwd: root,
      env: { ...process.env, PATH: `${bin}:${process.env.PATH}`, FORGE: "fixture-forge" },
    });
    const input = fs.readFileSync(path.join(root, "notes/generate-input.proto"), "utf8");
    for (const rpc of ["CreateNote", "GetNote", "UpdateNote", "DeleteNote", "ListNotes"]) {
      const declaration = input.match(new RegExp(`rpc ${rpc} \\([^]*?\\}`));
      assert.ok(declaration, `${rpc} must be scaffolded`);
      assert.match(declaration[0], /auth_required: false/);
    }
    assert.doesNotMatch(input, /auth_required: true/);
    assert.match(input, /message Note/);
    assert.equal(fs.existsSync(path.join(root, "notes/proto/services/notes/v1/notes.proto.bak")), false);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
