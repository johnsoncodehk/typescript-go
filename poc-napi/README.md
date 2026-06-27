# tsgo NAPI/FFI PoC

Proof of concept that `typescript-go` can run **fully in-process** inside
Node.js — eliminating the IPC transport that the current `@typescript/native-preview`
shim depends on.

## Thesis

The IPC shim (`tsgo` stdio/named-pipe API) is architecturally bottlenecked for
linter workloads: a linter makes **thousands** of cheap type/symbol queries,
and every one pays a transport tax of ~0.1–0.3 ms (stdio) / ~0.05–0.15 ms
(named pipe) — **on top of** the actual Go compiler work, which is only a few
microseconds for most queries. The IPC overhead dominates wall time by 10–40×.

The fix is to load the Go compiler as a shared library in the same process and
call it directly. No syscall, no context switch, no cross-process copy.

## How it works

```
┌─────────────────────────────────────────────────────────┐
│  Node.js process                                         │
│                                                          │
│   bridge.js  ──koffi FFI──►  bridge.dylib  (Go c-shared) │
│   (JS wrapper)                ├─ Cgo exports              │
│                               └─ api.Session.HandleRequest│
│                                    (same code path as the │
│                                     IPC server — zero     │
│                                     handler duplication)  │
└─────────────────────────────────────────────────────────┘
```

- `bridge.go` — `package main` built with `-buildmode=c-shared`. Exports five
  C functions: `BridgeNewSession`, `BridgeCall`, `BridgeCallBinary`,
  `BridgeDisposeSession`, plus two reusable buffers (one for JSON envelopes,
  one for raw binary). **Every** API method (`getTypeAtPosition`,
  `getSymbolAtLocation`, `getSourceFile`, `getSemanticDiagnostics`, …) is
  reached via `api.Session.HandleRequest(method, params)` — the exact same
  dispatch the stdio server uses. The only thing removed is the transport.
- `bridge.js` — tiny koffi wrapper. Two paths:
  - `call()` — JSON envelope `{"ok":true,"data":…}` / `{"ok":false,"error":"…"}`,
    auto-decoded by koffi to a JS string at the FFI boundary.
  - `callBinary()` — for `getSourceFile` etc.; returns a `struct {void* data;
    int64_t len}` by value, copied once into a Node `Buffer`. No base64
    round-trip (saves 33% + encode/decode). Calls are fully synchronous.
- Memory: the Go side keeps **one** reusable JSON buffer and **one** reusable
  binary buffer. Because calls are synchronous, koffi copies the data out at
  the FFI boundary before the next call reuses the buffer. Zero per-call
  malloc, zero leaks. `UseBinaryResponses=true` makes `getSourceFile`/`echo`
  return `api.RawBinary` straight through the binary path.

## Build & run

```bash
# from the typescript-go fork root
go build -buildmode=c-shared -o poc-napi/bridge.dylib ./poc-napi/

cd poc-napi
npm install
node test.js     # end-to-end: load tsconfig, type-check, type/symbol queries
node bench.js    # latency benchmark
```

`bridge.dylib` is ~25 MB (it statically embeds the `lib.*.d.ts` files and the
whole compiler). A `.h` header is generated alongside it.

## Benchmark (Apple Silicon, Node 24, Go 1.26)

```
ping (bridge floor)   : mean=0.001ms p50=0.001 p95=0.002 p99=0.004
getTypeAtPosition     : mean=0.007ms p50=0.006 p95=0.010 p99=0.029
getSymbolAtPosition   : mean=0.009ms p50=0.006 p95=0.012 p99=0.051

Throughput: ~157,000 getTypeAtPosition calls/sec (in-process)
```

| call                  | in-process | IPC (stdio) | IPC (pipe) | speedup |
| --------------------- | ---------: | ----------: | ---------: | ------: |
| ping (transport only) |   ~0.001ms |   ~0.10ms   |  ~0.05ms   |  50–100×|
| getTypeAtPosition     |   ~0.007ms |   ~0.12ms   |  ~0.07ms   |  10–17× |

The `ping` row isolates pure transport cost: the bridge floor is **~1 µs**,
versus ~50–300 µs for IPC. For real type queries, Go's actual work is ~7 µs —
IPC makes it 10–40× more expensive than it needs to be. This is the
"order-of-magnitude" the IPC shim could never reach.

## End-to-end: driving tsslint's real lint pipeline

The bridge is wired into tsslint's `poc-tsgo` harness as a third engine mode
(`runTsgoNapi()`). The integration reuses the entire `@typescript/native-preview`
JS API surface — `Snapshot`/`Project`/`Program`/`Checker` — by injecting an
`InProcessClient` (duck-typed replacement for the IPC `Client`). tsslint's
`createTsgoBackend` logic is **unchanged**; only the transport is swapped.

```
node packages/poc-tsgo/bench.js --runs=6   (micro: 1 file, syntactic no-console)

── PoC 引擎核心（同 process）──
  engine Strada:        169ms
  engine tsgo (IPC):     61ms
  engine tsgo NAPI:      48ms   (in-process, no IPC)

  tsgo(IPC)  / Strada  ≈ 0.36×
  tsgo(NAPI) / Strada  ≈ 0.28×   (3.5× faster than Strada)
  tsgo(IPC)  / tsgo(NAPI) ≈ 1.27×  (IPC tax on the lint pipeline)
```

Correctness: `hitsMatch(runStrada(), runTsgoNapi())` passes — the in-process
backend produces **identical diagnostics** to Strada (and to the IPC backend).
The IPC tax shows as 27% even on this tiny single-file *syntactic* workload;
on type-aware rules with thousands of type queries the NAPI advantage grows
proportionally, since each query sheds its ~50–300 µs transport cost.

See `packages/poc-tsgo/lib/tsgo-napi-client.js` for the `InProcessClient` +
`MiniSourceFileCache` + `createInProcessAPI` integration.

## What this proves / doesn't prove

**Proves:**
- The Go runtime initializes and runs correctly inside the Node process
  (GC, goroutine scheduler, bundled FS, project loading, the type checker).
- `api.Session.HandleRequest` is reusable as a direct in-process entry point —
  no handler duplication, the whole API surface is available for free.
- The latency ceiling for in-process type queries is **microseconds**, not
  hundreds of microseconds. The IPC transport, not Go, was the bottleneck.

**Does NOT prove (yet):**
- GC coordination under sustained load / long-lived processes.
- Object identity & lifetime for `Type`/`Symbol` handles across a real linter
  run (the IPC shim's `objectRegistry` pattern needs an in-process equivalent;
  here we just re-`JSON.parse` each response, which is fine for a PoC but
  wasteful for production).
- Concurrent calls from multiple Node threads (the bridge serializes via a
  mutex; a real NAPI addon would want thread-safe access to the checker pool).

## Path to a real NAPI addon (for tsslint + vue-tsc)

This PoC uses koffi (generic FFI) to call a C-shared library. A production
binding would replace the koffi hop with a proper NAPI addon for:

1. **Zero-copy binary transfer for `getSourceFile`.** The IPC shim already
   materializes AST nodes from a binary buffer (`RemoteNode`); a NAPI addon
   can hand that buffer to JS as a `Buffer`/`ArrayBuffer` view over Go memory
   (or a copy) with no base64 round-trip.
2. **Stable object identity.** Expose `Type`/`Symbol`/`Signature` as NAPI
   wrapper objects backed by Go registry IDs, with finalizers that release refs
   — mirroring the IPC `objectRegistry` but without per-call JSON.
3. **`typescript`-shaped surface.** Wrap the bridge so it looks like the
   `typescript` package (`ts.createProgram`, `checker.getTypeAtLocation`,
   `type.getSymbol()`, …). Then tsslint / vue-tsc swap `"typescript"` for the
   wrapper package and change nothing else.
4. **Prebuilt binaries.** Ship prebuilt `.node` artifacts per platform
   (the c-shared `.dylib`/`.so`/`.dll` plus a thin NAPI loader) so consumers
  don't need a Go toolchain.

The bridge in this PoC is the right **shape** for that addon: `BridgeCall` is
already the single dispatch point. The upgrade is mechanical — swap koffi for
NAPI, replace the JSON envelope with typed NAPI values, and add finalizers.

## Files

- `bridge.go` — Cgo exports (the only Go code added to the fork).
- `bridge.js` — koffi wrapper / JS API.
- `test.js` — end-to-end smoke test.
- `bench.js` — latency benchmark.
- `fixtures/` — a tiny TS project to lint against.
