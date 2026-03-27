'use strict';

// node_wasm_runner.js is a small JSON-over-stdio bridge used by the Go spec
// harness in tests/wasmspec/wasmspec_harness.go.
//
// Invocation:
//   node tests/wasmspec/node_wasm_runner.js
//
// Protocol:
// - The caller writes one JSON object per line to stdin.
// - The runner writes one JSON object per line to stdout.
// - Each request must include an "op" field selecting the operation.
// - Successful responses have { ok: true, ... }.
// - Failed responses have { ok: false, error: "..." }.
//
// Supported ops:
// - instantiate:
//     { op: "instantiate", moduleName, wasmBase64 }
//   Compiles and instantiates a module. When moduleName is non-empty, the
//   instance is registered under that name so later requests can invoke
//   exports or use it for imports. When moduleName is empty, the instance is
//   ephemeral and is not retained after instantiation returns.
// - validate:
//     { op: "validate", wasmBase64 }
//   Compiles a module without instantiating it.
// - invoke:
//     { op: "invoke", moduleName, funcName, args, resultTypes, ...helperFields }
//   Calls an exported function and returns encoded results. When helper fields
//   are present, the runner instantiates a Go-compiled helper module that wraps
//   the target export inside wasm before returning.
// - get:
//     { op: "get", moduleName, globalName, valueType }
//   Reads an exported global and returns its encoded value.
// - close:
//     { op: "close" }
//   Requests a clean shutdown.
//
// Value encoding:
// - Integer and float values are passed as decimal strings containing their raw
//   WebAssembly bit patterns.
// - "funcref" and "externref" may use { null: true } for null references.
// - Non-null externrefs are tracked through a stable in-process identity map.
//   The Go harness does not send a real JS object over JSON; instead it sends a
//   decimal-string identity token in the "bits" field. This runner uses that
//   token as a key in externRefs and materializes a unique JS object for it:
//     { __watgoExternRef: "<token>" }
//   If the same token is seen again later in the same Node process, the exact
//   same JS object instance is reused. That preserves JS/WebAssembly reference
//   identity for externref values across invocations within one .wast file.
//   When an exported externref global or function result comes back from
//   WebAssembly, encodeValue expects it to be one of these watgo-managed
//   objects; it then emits the original identity token back to Go in "bits".
//   This scheme is process-local and intentionally only guarantees stable
//   identity within a single runner lifetime.

const readline = require('node:readline');
const fs = require('node:fs');

// modules stores named module instances that remain live for the duration of a
// single .wast file run.
const modules = new Map();

// helperModules stores Go-compiled helper modules keyed by a stable signature.
const helperModules = new Map();

// externRefs stores stable JS objects keyed by the textual bit-pattern identity
// assigned by the Go harness for externref values.
const externRefs = new Map();

// opaqueExternRefs assigns stable synthetic identities to externref values that
// did not originate from a watgo-managed host token.
const opaqueExternRefs = new Map();
let nextOpaqueExternRefId = 1n;

// decodeBytes turns a base64-encoded wasm payload from the Go harness into raw
// bytes suitable for WebAssembly.Module / WebAssembly.Instance.
function decodeBytes(wasmBase64) {
  return Buffer.from(wasmBase64, 'base64');
}

// buildImports builds the import object visible to a new instantiation from all
// modules currently registered in this process.
function buildImports() {
  const imports = Object.create(null);
  for (const [name, record] of modules.entries()) {
    imports[name] = record.instance.exports;
  }
  return imports;
}

// toFloat32 decodes an f32 raw bit-pattern string into a JS number.
function toFloat32(bitsText) {
  const bits = Number.parseInt(bitsText, 10) >>> 0;
  const buf = new ArrayBuffer(4);
  const view = new DataView(buf);
  view.setUint32(0, bits, true);
  return view.getFloat32(0, true);
}

// toFloat64 decodes an f64 raw bit-pattern string into a JS number.
function toFloat64(bitsText) {
  const bits = BigInt(bitsText);
  const buf = new ArrayBuffer(8);
  const view = new DataView(buf);
  view.setBigUint64(0, bits, true);
  return view.getFloat64(0, true);
}

// fromFloat32 encodes a JS number into its f32 raw bit-pattern string.
function fromFloat32(value) {
  const buf = new ArrayBuffer(4);
  const view = new DataView(buf);
  view.setFloat32(0, value, true);
  return String(view.getUint32(0, true));
}

// fromFloat64 encodes a JS number into its f64 raw bit-pattern string.
function fromFloat64(value) {
  const buf = new ArrayBuffer(8);
  const view = new DataView(buf);
  view.setFloat64(0, value, true);
  return String(view.getBigUint64(0, true));
}

// getExternRef returns a stable JS object for an externref identity supplied by
// the Go harness, creating it on first use.
function getExternRef(bitsText) {
  const key = String(bitsText);
  let ref = externRefs.get(key);
  if (!ref) {
    ref = { __watgoExternRef: key };
    externRefs.set(key, ref);
  }
  return ref;
}

// getOpaqueExternRefId returns a stable synthetic identity string for an
// arbitrary JS value that crosses the bridge as a non-managed externref.
function getOpaqueExternRefId(value) {
  let id = opaqueExternRefs.get(value);
  if (!id) {
    id = String(nextOpaqueExternRefId);
    nextOpaqueExternRefId += 1n;
    opaqueExternRefs.set(value, id);
  }
  return id;
}

// decodeValue converts one JSON-encoded wasm value from the harness into the JS
// value expected by the WebAssembly JS API.
function decodeValue(arg) {
  switch (arg.type) {
    case 'i32':
      return Number.parseInt(arg.bits, 10) | 0;
    case 'i64':
      return BigInt.asIntN(64, BigInt(arg.bits));
    case 'f32':
      return toFloat32(arg.bits);
    case 'f64':
      return toFloat64(arg.bits);
    case 'funcref':
      if (arg.null) {
        return null;
      }
      throw new Error('non-null funcref arguments are not supported');
    case 'externref':
      if (arg.null) {
        return null;
      }
      return getExternRef(arg.bits);
    case 'anyref':
      if (arg.null) {
        return null;
      }
      if (arg.refKind === 'host') {
        return getExternRef(arg.bits);
      }
      throw new Error(`unsupported anyref argument kind ${arg.refKind ?? '<missing>'}`);
    case 'eqref':
    case 'i31ref':
    case 'structref':
    case 'arrayref':
    case 'nullref':
    case 'v128':
      if (arg.null) {
        return null;
      }
      throw new Error(`non-null ${arg.type} arguments require a wrapper path`);
    default:
      throw new Error(`unsupported value type ${arg.type}`);
  }
}

// encodeValue converts a JS value produced by the WebAssembly JS API into the
// JSON encoding expected by the Go harness.
function encodeValue(valueType, value) {
  switch (valueType) {
    case 'i32':
      return { type: valueType, bits: String(value >>> 0) };
    case 'i64':
      return { type: valueType, bits: String(BigInt.asUintN(64, value)) };
    case 'f32':
      return { type: valueType, bits: fromFloat32(value) };
    case 'f64':
      return { type: valueType, bits: fromFloat64(value) };
    case 'funcref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      return { type: valueType, bits: '1' };
    case 'externref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      if (typeof value === 'object' && value !== null && typeof value.__watgoExternRef === 'string') {
        return { type: valueType, refKind: 'extern', bits: value.__watgoExternRef };
      }
      return { type: valueType, refKind: 'extern', bits: getOpaqueExternRefId(value) };
    case 'anyref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      if (typeof value === 'object' && value !== null && typeof value.__watgoExternRef === 'string') {
        return { type: valueType, refKind: 'host', bits: value.__watgoExternRef };
      }
      if (typeof value === 'number') {
        return { type: valueType, refKind: 'i31' };
      }
      if (typeof WebAssembly.Struct === 'function' && value instanceof WebAssembly.Struct) {
        return { type: valueType, refKind: 'struct' };
      }
      if (typeof WebAssembly.Array === 'function' && value instanceof WebAssembly.Array) {
        return { type: valueType, refKind: 'array' };
      }
      return { type: valueType, refKind: 'eq' };
    case 'eqref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      return { type: valueType, refKind: 'eq' };
    case 'nullref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      throw new Error('non-null nullref result');
    case 'i31ref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      return { type: valueType, refKind: 'i31' };
    case 'structref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      return { type: valueType, refKind: 'struct' };
    case 'arrayref':
      if (value === null) {
        return { type: valueType, null: true };
      }
      return { type: valueType, refKind: 'array' };
    default:
      throw new Error(`unsupported value type ${valueType}`);
  }
}

// encodeResults normalizes a JS function return value into the array form used
// by the harness, then encodes each result with the corresponding wasm type.
function encodeResults(raw, resultTypes) {
  if (resultTypes.length === 0) {
    return [];
  }
  const values = resultTypes.length === 1 ? [raw] : raw;
  if (!Array.isArray(values)) {
    throw new Error(`expected multi-value result array, got ${typeof values}`);
  }
  if (values.length !== resultTypes.length) {
    throw new Error(`result arity mismatch: got ${values.length}, want ${resultTypes.length}`);
  }
  return values.map((value, i) => encodeValue(resultTypes[i], value));
}

// getModuleRecord looks up one previously registered module instance.
function getModuleRecord(moduleName) {
  const record = modules.get(moduleName);
  if (!record) {
    throw new Error(`module ${JSON.stringify(moduleName)} not found`);
  }
  return record;
}

// instantiate compiles and instantiates one wasm module. A non-empty
// moduleName keeps the instance available for later requests; an empty name
// makes this a one-shot instantiation used only for its success/failure.
function instantiate(moduleName, wasmBase64) {
  const bytes = decodeBytes(wasmBase64);
  const module = new WebAssembly.Module(bytes);
  const instance = new WebAssembly.Instance(module, buildImports());
  if (moduleName) {
    modules.set(moduleName, { module, instance });
  }
}

// getHelperModule looks up or compiles one Go-supplied helper module.
function getHelperModule(helperKey, wasmBase64) {
  let helperModule = helperModules.get(helperKey);
  if (!helperModule) {
    helperModule = new WebAssembly.Module(decodeBytes(wasmBase64));
    helperModules.set(helperKey, helperModule);
  }
  return helperModule;
}

// anyrefFromClassificationCode converts one helper-side classification code
// into the final JSON encoding expected by the Go harness.
function anyrefFromClassificationCode(code) {
  switch (code) {
    case 0:
      return { type: 'anyref', null: true };
    case 1:
      return { type: 'anyref', refKind: 'i31' };
    case 2:
      return { type: 'anyref', refKind: 'struct' };
    case 3:
      return { type: 'anyref', refKind: 'array' };
    default:
      return null;
  }
}

// invokeWithHelper instantiates one cached helper against the target export and
// invokes its selected export with helper-visible arguments.
function invokeWithHelper(targetFn, msg) {
  const helperModule = getHelperModule(msg.helperKey, msg.helperWasmBase64);
  const helperInstance = new WebAssembly.Instance(helperModule, { m: { f: targetFn } });
  const helperFn = helperInstance.exports[msg.helperFuncName || 'call'];
  if (typeof helperFn !== 'function') {
    throw new Error(`helper export ${JSON.stringify(msg.helperFuncName || 'call')} not found`);
  }
  const helperArgs = (msg.helperArgs || []).map(decodeValue);
  if (msg.helperMode === 'anyref_classify') {
    const code = helperFn(...helperArgs);
    const classified = anyrefFromClassificationCode(code);
    if (classified) {
      return { ok: true, results: [classified] };
    }
    const rawFn = helperInstance.exports.call_raw;
    if (typeof rawFn !== 'function') {
      throw new Error('helper export "call_raw" not found');
    }
    return { ok: true, results: [encodeValue('anyref', rawFn(...helperArgs))] };
  }
  const raw = helperFn(...helperArgs);
  return { ok: true, results: encodeResults(raw, msg.helperResultTypes || []) };
}

// handleMessage executes a single JSON request and returns the JSON response
// body that should be written to stdout. When 'exit' is true in the response,
// the caller should cleanly shut down after writing the response.
function handleMessage(msg) {
  switch (msg.op) {
    case 'instantiate':
      instantiate(msg.moduleName || '', msg.wasmBase64);
      return { ok: true };
    case 'validate': {
      const bytes = decodeBytes(msg.wasmBase64);
      new WebAssembly.Module(bytes);
      return { ok: true };
    }
    case 'invoke': {
      const record = getModuleRecord(msg.moduleName);
      const targetFn = record.instance.exports[msg.funcName];
      if (typeof targetFn !== 'function') {
        throw new Error(`exported function ${JSON.stringify(msg.funcName)} not found`);
      }
      if (msg.helperWasmBase64) {
        return invokeWithHelper(targetFn, msg);
      }
      const rawArgs = msg.args || [];
      const args = rawArgs.map(decodeValue);
      const raw = targetFn(...args);
      return { ok: true, results: encodeResults(raw, msg.resultTypes || []) };
    }
    case 'get': {
      const record = getModuleRecord(msg.moduleName);
      const g = record.instance.exports[msg.globalName];
      if (!(g instanceof WebAssembly.Global)) {
        throw new Error(`exported global ${JSON.stringify(msg.globalName)} not found`);
      }
      return { ok: true, result: encodeValue(msg.valueType, g.value) };
    }
    case 'close':
      return { ok: true, exit: true };
    default:
      throw new Error(`unsupported op ${JSON.stringify(msg.op)}`);
  }
}

// rl reads one JSON request per line from stdin.
const rl = readline.createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});

// Process each request line synchronously, emit a single response line, and
// handle shutdown when requested. Use a synchronous write so the close path
// does not depend on an async stdout callback before exiting.
rl.on('line', (line) => {
  if (!line.trim()) {
    return;
  }
  let response;
  try {
    const msg = JSON.parse(line);
    response = handleMessage(msg);
  } catch (err) {
    response = {
      ok: false,
      error: err && err.message ? err.message : String(err),
    };
  }
  fs.writeSync(process.stdout.fd, JSON.stringify(response) + '\n');
  if (response.exit) {
    rl.close();
    process.exit(0);
  }
});
