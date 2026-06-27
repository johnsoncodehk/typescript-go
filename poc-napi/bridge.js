// JS wrapper around the c-shared bridge.dylib. Exposes a small,
// fully synchronous API that mirrors the subset of tsgo's API we need for the
// POC. Every call is a direct in-process function call — no IPC.
//
// Wire format: the bridge returns a single JSON envelope string per call:
//   {"ok":true,"data":<result>}  |  {"ok":false,"error":"..."}
//
// Memory: the Go side keeps one reusable result buffer (calls are synchronous,
// so koffi copies the NUL-terminated string into a JS string at the FFI
// boundary before the next call reuses the buffer). No per-call free needed.
import koffi from "koffi";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { existsSync } from "node:fs";

const __dirname = dirname(fileURLToPath(import.meta.url));

function libPath() {
  const candidates = [
    join(__dirname, "bridge.dylib"),
    join(__dirname, "bridge.so"),
    join(__dirname, "bridge.dll"),
  ];
  for (const p of candidates) {
    if (existsSync(p)) return p;
  }
  throw new Error(`bridge shared library not found under ${__dirname}`);
}

const lib = koffi.load(libPath());

// `char *` returns are auto-decoded by koffi to JS strings (copied at the FFI
// boundary), which is exactly what we want given the reusable-buffer protocol.
const BridgeNewSession = lib.func("char *BridgeNewSession(char *cwd)");
const BridgeCall = lib.func(
  "char *BridgeCall(int64_t session, char *method, char *paramsJson)"
);
const BridgeDisposeSession = lib.func("void BridgeDisposeSession(int64_t session)");

// Binary path: returns a struct by value { void* data; int64_t len }. The data
// pointer is into Go's reusable binary buffer and stays valid only until the
// next bridge call — so we copy into a Node Buffer immediately.
const BridgeBinary = koffi.struct("BridgeBinary", { data: "void *", len: "int64_t" });
const BridgeCallBinary = lib.func(
  "BridgeBinary BridgeCallBinary(int64_t session, char *method, char *paramsJson)"
);

export class BridgeError extends Error {
  constructor(message) {
    super(message);
    this.name = "BridgeError";
  }
}

function parseEnvelope(str) {
  if (str == null) {
    throw new BridgeError("bridge returned null");
  }
  const env = JSON.parse(str);
  if (!env.ok) {
    throw new BridgeError(env.error ?? "unknown bridge error");
  }
  return env.data ?? null;
}

function toCStr(s) {
  return Buffer.from(s + "\0", "utf8");
}

export function createSession(cwd) {
  const str = BridgeNewSession(toCStr(cwd));
  const handle = parseEnvelope(str);
  return Number(handle);
}

export function disposeSession(handle) {
  BridgeDisposeSession(BigInt(handle));
}

/**
 * Call a tsgo API method directly in-process.
 * @param {number} handle
 * @param {string} method  e.g. "getTypeAtPosition"
 * @param {object|string|null} params  object will be JSON-stringified
 * @returns {any} parsed JSON result (the env.data field)
 */
export function call(handle, method, params) {
  const paramsJson =
    params == null ? null : typeof params === "string" ? params : JSON.stringify(params);
  const str = BridgeCall(
    BigInt(handle),
    toCStr(method),
    paramsJson == null ? null : toCStr(paramsJson)
  );
  return parseEnvelope(str);
}

/**
 * Call a tsgo API method that returns raw bytes (e.g. getSourceFile) directly
 * in-process. No base64 round-trip — the Go handler returns api.RawBinary and
 * the bridge hands the bytes straight through. The result is copied once into
 * a Node Buffer at the FFI boundary.
 *
 * If the handler returns a non-binary result, this returns the JSON-marshaled
 * bytes as a Buffer; callers should use call() for those methods instead.
 * On bridge error, the returned Buffer contains a JSON error envelope
 * {"ok":false,"error":"..."} — check with maybeBridgeError().
 *
 * @param {number} handle
 * @param {string} method  e.g. "getSourceFile"
 * @param {object|string|null} params
 * @returns {Buffer} raw bytes (length 0 if the handler returned nil/empty)
 */
export function callBinary(handle, method, params) {
  const paramsJson =
    params == null ? null : typeof params === "string" ? params : JSON.stringify(params);
  const res = BridgeCallBinary(
    BigInt(handle),
    toCStr(method),
    paramsJson == null ? null : toCStr(paramsJson)
  );
  const len = Number(res.len);
  if (len <= 0 || res.data === null || res.data === 0) {
    return Buffer.alloc(0);
  }
  // koffi decodes a uint8 array view at the pointer; wrap in a Node Buffer.
  const arr = koffi.decode(res.data, koffi.array("uint8_t", len));
  return Buffer.from(arr);
}

/**
 * Inspect a Buffer returned by callBinary for a JSON error envelope. Throws a
 * BridgeError if it matches; otherwise returns the buffer unchanged. Use only
 * when you expect the method might surface an error via the binary path.
 */
export function maybeBridgeError(buf) {
  if (buf.length === 0 || buf[0] !== 0x7b /* '{' */) return buf;
  try {
    const env = JSON.parse(buf.toString("utf8"));
    if (env && env.ok === false) {
      throw new BridgeError(env.error ?? "unknown bridge error");
    }
  } catch (e) {
    if (e instanceof BridgeError) throw e;
    // not JSON — treat as raw binary
  }
  return buf;
}
