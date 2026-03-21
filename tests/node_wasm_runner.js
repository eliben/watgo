'use strict';

// node_wasm_runner.js is a small JSON-over-stdio bridge used by the Go spec
// harness in tests/wasmspec_harness.go.
//
// Invocation:
//   node tests/node_wasm_runner.js
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
//     { op: "invoke", moduleName, funcName, args, resultTypes }
//   Calls an exported function and returns encoded results.
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
// - Non-null externrefs are tracked through a stable in-process identity map so
//   the Go harness can round-trip them through later calls.

const readline = require('node:readline');

// modules stores named module instances that remain live for the duration of a
// single .wast file run.
const modules = new Map();

// externRefs stores stable JS objects keyed by the textual bit-pattern identity
// assigned by the Go harness for externref values.
const externRefs = new Map();

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
        return { type: valueType, bits: value.__watgoExternRef };
      }
      throw new Error('externref result is not a watgo-managed reference');
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

// handleMessage executes a single JSON request and returns the JSON response
// body that should be written to stdout.
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
      const fn = record.instance.exports[msg.funcName];
      if (typeof fn !== 'function') {
        throw new Error(`exported function ${JSON.stringify(msg.funcName)} not found`);
      }
      const args = (msg.args || []).map(decodeValue);
      const raw = fn(...args);
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
// exit cleanly when the caller sends the "close" operation.
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
  process.stdout.write(JSON.stringify(response) + '\n');
  if (response.exit) {
    rl.close();
    process.exit(0);
  }
});
