// POC test: drives the in-process tsgo bridge end-to-end.
//
// Flow:
//   1. createSession(cwd)
//   2. updateSnapshot(openProjects=[tsconfig], openFiles=[sample.ts])
//   3. getSourceFileNames()  -> confirm our file is in the program
//   4. getSemanticDiagnostics(file) -> confirm type-checking ran in-process
//   5. getTypeAtPosition(file, pos) -> the key type query, no IPC
//   6. getSourceFile(file) -> binary AST buffer (base64 in JSON mode)
//   7. disposeSession()
import { createSession, disposeSession, call, callBinary } from "./bridge.js";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const fixtureDir = join(__dirname, "fixtures");
const tsconfig = join(fixtureDir, "tsconfig.json");
const sampleFile = join(fixtureDir, "sample.ts");

function lineColToOffset(text, line0, col) {
  let off = 0;
  for (let i = 0; i < line0; i++) {
    off = text.indexOf("\n", off) + 1;
  }
  return off + col;
}

const src = readFileSync(sampleFile, "utf8");

console.log("== tsgo-napi POC ==");

// 1. create session
const t0 = performance.now();
const session = createSession(fixtureDir);
console.log(`session created: handle=${session} (${(performance.now() - t0).toFixed(1)}ms)`);

try {
  // 2. load project + file
  const t1 = performance.now();
  const upd = call(session, "updateSnapshot", {
    openProjects: [tsconfig],
    openFiles: [sampleFile],
  });
  const snapshot = upd.snapshot;
  const project = upd.projects[0].id; // project handle string (ProjectID)
  console.log(
    `updateSnapshot: snapshot=${snapshot}, project=${project} (${(performance.now() - t1).toFixed(1)}ms)`
  );
  console.log(`  projects: ${upd.projects.map((p) => p.id).join(", ")}`);

  // 3. source file names
  const names = call(session, "getSourceFileNames", { snapshot, project });
  const ours = names.filter((n) => n.endsWith("sample.ts"));
  console.log(`getSourceFileNames: ${names.length} files; ours=${ours.length}`);
  if (ours.length === 0) {
    console.error("  !! sample.ts not in program. names:", names.slice(0, 8));
    process.exit(1);
  }

  // 4. semantic diagnostics — proves the Go checker ran in-process
  const diags = call(session, "getSemanticDiagnostics", { snapshot, project, file: sampleFile }) ?? [];
  console.log(`getSemanticDiagnostics: ${diags.length} diagnostics`);
  for (const d of diags.slice(0, 5)) {
    console.log(`  - code ${d.code}: ${d.messageText}`);
  }

  // 5. type query at `greeting` identifier (line 0, col 6)
  const posGreeting = lineColToOffset(src, 0, 6);
  const t2 = performance.now();
  const typeGreeting = call(session, "getTypeAtPosition", {
    snapshot,
    project,
    file: sampleFile,
    position: posGreeting,
  });
  console.log(
    `getTypeAtPosition(greeting @${posGreeting}): ${JSON.stringify(typeGreeting)} (${(performance.now() - t2).toFixed(2)}ms)`
  );

  // type query at `total` (line 16, col 6)
  const posTotal = lineColToOffset(src, 16, 6);
  const typeTotal = call(session, "getTypeAtPosition", {
    snapshot,
    project,
    file: sampleFile,
    position: posTotal,
  });
  console.log(`getTypeAtPosition(total @${posTotal}): ${JSON.stringify(typeTotal)}`);

  // symbol query at `add` (line 9, col 10)
  const posAdd = lineColToOffset(src, 9, 10);
  const symAdd = call(session, "getSymbolAtPosition", {
    snapshot,
    project,
    file: sampleFile,
    position: posAdd,
  });
  console.log(`getSymbolAtPosition(add @${posAdd}): ${JSON.stringify(symAdd)}`);

  // 6. source file binary — raw bytes via BridgeCallBinary (no base64).
  const sfBuf = callBinary(session, "getSourceFile", { snapshot, project, file: sampleFile });
  console.log(`getSourceFile (binary): ${sfBuf.length} bytes of encoded AST`);
  if (sfBuf.length === 0 || sfBuf[0] === 0x7b) {
    console.log(`  !! unexpected: ${sfBuf.toString("utf8").slice(0, 120)}`);
  }

  console.log("\nPOC OK — tsgo ran fully in-process, zero IPC.");
} finally {
  disposeSession(session);
}
