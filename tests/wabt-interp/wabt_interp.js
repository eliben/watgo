'use strict';

// wabt_interp.js is the execution-side half of tests/wabt-interp.
//
// It emulates the narrow subset of WABT's `run-interp` behavior that this test
// package currently uses:
// - instantiate one compiled wasm module
// - optionally synthesize simple host imports such as `--host-print` and
//   `--dummy-import-func`
// - invoke the requested exports in order
// - return raw results plus any auxiliary stdout/stderr as JSON so the Go side
//   can apply the final WABT-style formatting and comparison rules
//
// Input:
//   node wabt_interp.js <payload.json>
//
// The payload is written by wabt_interp_test.go and describes the execution
// plan for one fixture:
// - `wasmPath`: compiled module to instantiate
// - `exports`: exported names and their param/result kinds
// - `imports`: imported function signatures for synthetic host shims
// - `invocations`: which exports to call, in what order, with which args
// - `hostPrint`, `dummyImportFunc`, `hostPrintResultKind`: small WABT
//   run-interp options modeled by this runner
//
// Output:
//   JSON on stdout with `stdout`, `stderr`, `exitCode`, and per-invocation
//   `results`. Successful numeric results are returned as raw bit patterns so
//   the Go side can format floats exactly the way the WABT fixtures expect.

const fs = require('node:fs');

function fail(message) {
  process.stderr.write(String(message) + '\n');
  process.exit(1);
}

if (process.argv.length !== 3) {
  fail('usage: node wabt_interp.js <payload.json>');
}

const payload = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const wasmPath = payload.wasmPath;
const exportsToRun = payload.exports;
const importedFuncs = payload.imports;
const invocations = payload.invocations;
const hostPrint = payload.hostPrint;
const dummyImportFunc = payload.dummyImportFunc;
const hostPrintResultKind = payload.hostPrintResultKind;

function valueString(kind, value) {
  const buf = new ArrayBuffer(8);
  const view = new DataView(buf);
  switch (kind) {
    case 'void':
      return '';
    case 'i32':
      return String(value >>> 0);
    case 'i64':
      return String(BigInt.asUintN(64, value));
    case 'f32':
      view.setFloat32(0, value, true);
      return String(view.getUint32(0, true));
    case 'f64':
      view.setFloat64(0, value, true);
      return String(view.getBigUint64(0, true));
    case 'funcref':
    case 'externref':
      if (value === null) {
        return '0';
      }
      if (kind === 'funcref') {
        return String(value.name || 1);
      }
      return '1';
    default:
      throw new Error('unsupported result kind: ' + kind);
  }
}

function normalizeTrapMessage(message) {
  if (message.includes('table index is out of bounds')) {
    return 'undefined table index';
  }
  if (message.includes('function signature mismatch') || message.includes('null function')) {
    return 'indirect call signature mismatch';
  }
  if (message.includes('unreachable')) {
    return 'unreachable executed';
  }
  return message;
}

function decodeArg(arg) {
  switch (arg.kind) {
    case 'i32':
      return Number.parseInt(arg.text, 10) | 0;
    case 'i64':
      return BigInt(arg.text);
    case 'f32':
    case 'f64':
      return Number(arg.text);
    default:
      throw new Error('unsupported invocation arg kind: ' + arg.kind);
  }
}

function formatArg(arg) {
  return arg.kind + ':' + arg.text;
}

function formatHostArg(value) {
  if (typeof value === 'bigint') {
    return 'i64:' + String(BigInt.asUintN(64, value));
  }
  if (typeof value === 'number') {
    return 'i32:' + String(value >>> 0);
  }
  if (value === null) {
    return 'externref:0';
  }
  return 'externref:1';
}

function formatArgByKind(kind, value) {
  switch (kind) {
    case 'i32':
      return 'i32:' + String(value >>> 0);
    case 'i64':
      return 'i64:' + String(BigInt.asUintN(64, value));
    case 'f32':
      return 'f32:' + Number(value).toFixed(6);
    case 'f64':
      return 'f64:' + Number(value).toFixed(6);
    case 'funcref':
      return 'funcref:' + (value === null ? '0' : '1');
    case 'externref':
      return 'externref:' + (value === null ? '0' : '1');
    default:
      throw new Error('unsupported import arg kind: ' + kind);
  }
}

function zeroValue(kind) {
  switch (kind) {
    case 'void':
      return undefined;
    case 'i32':
      return 0;
    case 'i64':
      return 0n;
    case 'f32':
    case 'f64':
      return 0;
    case 'funcref':
    case 'externref':
      return null;
    default:
      throw new Error('unsupported import result kind: ' + kind);
  }
}

const stdout = [];
const stderr = [];
const imports = hostPrint ? {
  host: {
    print: (...args) => {
      const formattedArgs = args.map(formatHostArg).join(', ');
      if (hostPrintResultKind === 'void' || hostPrintResultKind === '') {
        stdout.push('called host host.print(' + formattedArgs + ') =>');
        return 0;
      }
      stdout.push('called host host.print(' + formattedArgs + ') => ' + hostPrintResultKind + ':0');
      return 0;
    }
  }
} : {};

if (dummyImportFunc) {
  for (const imported of importedFuncs) {
    if (!imports[imported.module]) {
      imports[imported.module] = {};
    }
    imports[imported.module][imported.name] = (...args) => {
      const formattedArgs = args.map((arg, i) => formatArgByKind(imported.paramKinds[i], arg)).join(', ');
      const suffix = imported.resultKind === 'void' ? '' : ' ' + imported.resultKind + ':0';
      stdout.push('called host ' + imported.module + '.' + imported.name + '(' + formattedArgs + ') =>' + suffix);
      return zeroValue(imported.resultKind);
    };
  }
}

WebAssembly.instantiate(fs.readFileSync(wasmPath), imports).then(({ instance }) => {
  const results = [];
  for (const invocation of invocations) {
    const entry = exportsToRun.find((exp) => exp.name === invocation.exportName);
    if (!entry) {
      stderr.push('unknown export ' + invocation.exportName);
      process.stdout.write(JSON.stringify({ stdout, stderr, exitCode: 1, results }));
      return;
    }
    if (entry.kind !== 'func') {
      stdout.push("Export '" + invocation.exportName + "' is not a function");
      process.stdout.write(JSON.stringify({ stdout, stderr, exitCode: 1, results }));
      return;
    }
    const fn = instance.exports[entry.name];
    const args = invocation.args || [];
    const jsArgs = args.map(decodeArg);
    const argText = args.map(formatArg).join(', ');
    try {
      const result = fn(...jsArgs);
      results.push({
        name: entry.name,
        resultKind: entry.resultKind,
        argText,
        value: valueString(entry.resultKind, result),
        error: '',
        stdoutCount: stdout.length,
      });
    } catch (err) {
      results.push({
        name: entry.name,
        resultKind: entry.resultKind,
        argText,
        value: '',
        error: normalizeTrapMessage(String(err && err.message ? err.message : err)),
        stdoutCount: stdout.length,
      });
    }
  }
  process.stdout.write(JSON.stringify({ stdout, stderr, exitCode: 0, results }));
}).catch((err) => {
  const message = normalizeTrapMessage(String(err && err.message ? err.message : err));
  const prefix = message === 'unreachable executed' ? 'error initializing module: ' : '';
  process.stdout.write(JSON.stringify({ stdout: [], stderr: [prefix + message], exitCode: 1, results: [] }));
});
