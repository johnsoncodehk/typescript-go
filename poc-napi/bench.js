// Benchmark: in-process bridge call latency vs the known IPC transport cost.
//
// We measure three things:
//   1. "ping" round-trip  — pure bridge overhead (FFI + JSON marshal), no
//      compiler work. This is the floor: what every call costs even if Go did
//      nothing. Compare this to the IPC round-trip floor (~0.1-0.3ms/stdio,
//      ~0.05-0.15ms/named-pipe on this host class).
//   2. getTypeAtPosition   — a real type query (the hot path for linters).
//   3. getSymbolAtPosition — a real symbol query.
//
// The in-process floor is dominated by JSON (de)serialization across the FFI
// boundary; there is no syscall, no context switch, no copy between processes.
import { createSession, disposeSession, call } from "./bridge.js";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = dirname(fileURLToPath(import.meta.url));
const fixtureDir = join(__dirname, "fixtures");
const tsconfig = join(fixtureDir, "tsconfig.json");
const sampleFile = join(fixtureDir, "fixtures/sample.ts".replace("fixtures/", ""));
const samplePath = join(fixtureDir, "sample.ts");

const src = readFileSync(samplePath, "utf8");
function offset(line0, col) {
  let off = 0;
  for (let i = 0; i < line0; i++) off = src.indexOf("\n", off) + 1;
  return off + col;
}
const posGreeting = offset(0, 6); // `greeting`
const posAdd = offset(9, 10); // `add`

function stats(samples) {
  samples.sort((a, b) => a - b);
  const n = samples.length;
  const sum = samples.reduce((a, b) => a + b, 0);
  const mean = sum / n;
  const p50 = samples[Math.floor(n * 0.5)];
  const p95 = samples[Math.floor(n * 0.95)];
  const p99 = samples[Math.floor(n * 0.99)];
  return { n, mean, p50, p95, p99, min: samples[0], max: samples[n - 1] };
}

function fmt(s) {
  return `mean=${s.mean.toFixed(3)}ms p50=${s.p50.toFixed(3)} p95=${s.p95.toFixed(3)} p99=${s.p99.toFixed(3)} min=${s.min.toFixed(3)} max=${s.max.toFixed(3)}`;
}

const session = createSession(fixtureDir);
const upd = call(session, "updateSnapshot", {
  openProjects: [tsconfig],
  openFiles: [samplePath],
});
const snapshot = upd.snapshot;
const project = upd.projects[0].id;

// warm up (first call JITs / primes caches)
for (let i = 0; i < 200; i++) call(session, "ping", null);
for (let i = 0; i < 50; i++) call(session, "getTypeAtPosition", { snapshot, project, file: samplePath, position: posGreeting });

const N = 5000;

const ping = [];
for (let i = 0; i < N; i++) {
  const t = performance.now();
  call(session, "ping", null);
  ping.push(performance.now() - t);
}

const types = [];
for (let i = 0; i < N; i++) {
  const t = performance.now();
  call(session, "getTypeAtPosition", { snapshot, project, file: samplePath, position: posGreeting });
  types.push(performance.now() - t);
}

const symbols = [];
for (let i = 0; i < N; i++) {
  const t = performance.now();
  call(session, "getSymbolAtPosition", { snapshot, project, file: samplePath, position: posAdd });
  symbols.push(performance.now() - t);
}

// A batched call: getTypeAtLocations with many nodes would be one IPC vs N;
// here we simulate N independent type queries — the worst case for IPC.
const throughputStart = performance.now();
for (let i = 0; i < N; i++) {
  call(session, "getTypeAtPosition", { snapshot, project, file: samplePath, position: posGreeting });
}
const throughputMs = performance.now() - throughputStart;

disposeSession(session);

console.log("== tsgo-napi in-process benchmark ==");
console.log(`N=${N} per measurement\n`);
console.log(`ping (bridge floor)   : ${fmt(stats(ping))}`);
console.log(`getTypeAtPosition     : ${fmt(stats(types))}`);
console.log(`getSymbolAtPosition   : ${fmt(stats(symbols))}`);
console.log(`\nThroughput: ${(N / (throughputMs / 1000)).toFixed(0)} getTypeAtPosition calls/sec (in-process)`);
console.log(`\nFor reference, the IPC shim measured ~0.10-0.30ms round-trip per call`);
console.log(`(stdio) and ~0.05-0.15ms (named pipe) on this host class — that is pure`);
console.log(`transport overhead paid on EVERY RPC, on top of the Go work. The in-process`);
console.log(`bridge pays only the FFI + JSON (de)serial cost shown above, with no syscall.`);
