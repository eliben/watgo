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

// modules stores named module instances that remain live for the duration of a
// single .wast file run.
const modules = new Map();

// externRefs stores stable JS objects keyed by the textual bit-pattern identity
// assigned by the Go harness for externref values.
const externRefs = new Map();

// floatResultWrappers caches tiny helper modules used to preserve exact float
// result bits across the Wasm/JS boundary. JS numbers do not preserve NaN
// payloads, so when a wasm export returns a single f32/f64 result we route the
// call through a generated wrapper module that:
//   1. imports the target export as a wasm function,
//   2. calls it with the original arguments, and
//   3. reinterprets the float result to i32/i64 inside wasm.
//
// Example wrapper for an imported `(func (param i64) (result f32))`:
//   (module
//     (import "m" "f" (func (param i64) (result f32)))
//     (func (export "call") (param i64) (result i32)
//       local.get 0
//       call 0
//       i32.reinterpret_f32))
//
// The wrapper returns integer bits, which JS can transport exactly.
const floatResultWrappers = new Map();

// decodeBytes turns a base64-encoded wasm payload from the Go harness into raw
// bytes suitable for WebAssembly.Module / WebAssembly.Instance.
function decodeBytes(wasmBase64) {
  return Buffer.from(wasmBase64, 'base64');
}

// encodeULEB128 appends one unsigned LEB128 integer to bytes.
function encodeULEB128(bytes, value) {
  let v = Number(value);
  do {
    let byte = v & 0x7f;
    v >>>= 7;
    if (v !== 0) {
      byte |= 0x80;
    }
    bytes.push(byte);
  } while (v !== 0);
}

// encodeName appends one wasm name string.
function encodeName(bytes, text) {
  const utf8 = Buffer.from(text, 'utf8');
  encodeULEB128(bytes, utf8.length);
  bytes.push(...utf8);
}

// valueTypeCode returns the binary valtype encoding for one supported type.
function valueTypeCode(type) {
  switch (type) {
    case 'i32':
      return 0x7f;
    case 'i64':
      return 0x7e;
    case 'f32':
      return 0x7d;
    case 'f64':
      return 0x7c;
    case 'funcref':
      return 0x70;
    case 'externref':
      return 0x6f;
    default:
      throw new Error(`unsupported wrapper value type ${type}`);
  }
}

// pushFuncType appends one wasm functype to bytes.
function pushFuncType(bytes, paramTypes, resultTypes) {
  bytes.push(0x60);
  encodeULEB128(bytes, paramTypes.length);
  for (const type of paramTypes) {
    bytes.push(valueTypeCode(type));
  }
  encodeULEB128(bytes, resultTypes.length);
  for (const type of resultTypes) {
    bytes.push(valueTypeCode(type));
  }
}

// pushSection appends one complete wasm section.
function pushSection(bytes, id, payload) {
  bytes.push(id);
  encodeULEB128(bytes, payload.length);
  bytes.push(...payload);
}

// reinterpretOpcode returns the wasm reinterpret instruction used to turn a
// single float result into exact integer bits.
function reinterpretOpcode(resultType) {
  switch (resultType) {
    case 'f32':
      return 0xbc; // i32.reinterpret_f32
    case 'f64':
      return 0xbd; // i64.reinterpret_f64
    default:
      throw new Error(`unsupported float wrapper result ${resultType}`);
  }
}

// intBitsTypeForFloat maps one float result type to the integer type carrying
// its exact IEEE-754 bits.
function intBitsTypeForFloat(resultType) {
  switch (resultType) {
    case 'f32':
      return 'i32';
    case 'f64':
      return 'i64';
    default:
      throw new Error(`unsupported float wrapper result ${resultType}`);
  }
}

// buildSingleFloatResultWrapper builds a minimal wasm module that imports one
// function and reinterprets its single float result to raw integer bits.
function buildSingleFloatResultWrapper(paramTypes, resultType) {
  const bitsType = intBitsTypeForFloat(resultType);
  const key = `${paramTypes.join(',')}->${resultType}`;
  let cached = floatResultWrappers.get(key);
  if (cached) {
    return cached;
  }

  const bytes = [
    0x00, 0x61, 0x73, 0x6d, // \0asm
    0x01, 0x00, 0x00, 0x00, // version 1
  ];

  const typeSection = [];
  encodeULEB128(typeSection, 2);
  pushFuncType(typeSection, paramTypes, [resultType]);
  pushFuncType(typeSection, paramTypes, [bitsType]);
  pushSection(bytes, 1, typeSection);

  const importSection = [];
  encodeULEB128(importSection, 1);
  encodeName(importSection, 'm');
  encodeName(importSection, 'f');
  importSection.push(0x00); // function import
  encodeULEB128(importSection, 0);
  pushSection(bytes, 2, importSection);

  const functionSection = [];
  encodeULEB128(functionSection, 1);
  encodeULEB128(functionSection, 1);
  pushSection(bytes, 3, functionSection);

  const exportSection = [];
  encodeULEB128(exportSection, 1);
  encodeName(exportSection, 'call');
  exportSection.push(0x00); // function export
  encodeULEB128(exportSection, 1);
  pushSection(bytes, 7, exportSection);

  const body = [];
  encodeULEB128(body, 0); // local decl count
  for (let i = 0; i < paramTypes.length; i++) {
    body.push(0x20); // local.get
    encodeULEB128(body, i);
  }
  body.push(0x10, 0x00); // call 0
  body.push(reinterpretOpcode(resultType));
  body.push(0x0b); // end

  const codeSection = [];
  encodeULEB128(codeSection, 1);
  encodeULEB128(codeSection, body.length);
  codeSection.push(...body);
  pushSection(bytes, 10, codeSection);

  cached = Uint8Array.from(bytes);
  floatResultWrappers.set(key, cached);
  return cached;
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

// encodeSingleFloatResultPreservingBits routes one single-result f32/f64 call
// through a tiny wasm wrapper so exact NaN payloads survive the bridge back to
// the Go harness.
function encodeSingleFloatResultPreservingBits(fn, args, argTypes, resultType) {
  const wrapperBytes = buildSingleFloatResultWrapper(argTypes, resultType);
  const wrapperModule = new WebAssembly.Module(wrapperBytes);
  const wrapperInstance = new WebAssembly.Instance(wrapperModule, { m: { f: fn } });
  const bits = wrapperInstance.exports.call(...args);
  if (resultType === 'f32') {
    return { type: resultType, bits: String(bits >>> 0) };
  }
  return { type: resultType, bits: String(BigInt.asUintN(64, bits)) };
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
      const fn = record.instance.exports[msg.funcName];
      if (typeof fn !== 'function') {
        throw new Error(`exported function ${JSON.stringify(msg.funcName)} not found`);
      }
      const rawArgs = msg.args || [];
      const args = rawArgs.map(decodeValue);
      const resultTypes = msg.resultTypes || [];
      if (resultTypes.length === 1 && (resultTypes[0] === 'f32' || resultTypes[0] === 'f64')) {
        const argTypes = rawArgs.map((arg) => arg.type);
        return {
          ok: true,
          results: [encodeSingleFloatResultPreservingBits(fn, args, argTypes, resultTypes[0])],
        };
      }
      const raw = fn(...args);
      return { ok: true, results: encodeResults(raw, resultTypes) };
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
// handle shutdown when requested.
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
