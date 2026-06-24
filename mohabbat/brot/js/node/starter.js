import { readlinkSync, realpathSync } from 'node:fs';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';

// Patched by buildNodeSlot:
const VERBOSE = false; // {{VERBOSE}}
const L = (...args) => (VERBOSE || process.env.MOHABBAT_VERBOSE) && console.error(...args);

async function run() {
  L("[starter] run starting");
  const isMain = process.argv[1] && (process.argv[1] === '-' || (import.meta.url.startsWith('file:') && fileURLToPath(import.meta.url) === fs.realpathSync(process.argv[1])));
  if (!isMain) {
    L("[starter] not main");
    return;
  }

  const vegPath = process.argv[2];
  if (!vegPath) {
    console.error("[mohabbat] no vegetable path provided");
    process.exit(1);
  }
  L("[starter] vegPath:", vegPath);

  const fd = fs.openSync(vegPath, 'r');
  const stats = fs.fstatSync(fd);
  const fileSize = stats.size;
  L("[starter] fileSize:", fileSize);
  
  const poolLen = "{{NODE_POOL_LEN}}";
  L("[starter] poolLen:", poolLen);
  if (poolLen === 0) {
    fs.closeSync(fd);
    console.error("[mohabbat] invalid pool length");
    process.exit(4);
  }

  const poolStart = fileSize - poolLen;
  L("[starter] poolStart:", poolStart);
  const compressedData = Buffer.alloc(poolLen);
  fs.readSync(fd, compressedData, 0, poolLen, poolStart);
  fs.closeSync(fd);
  L("[starter] pool read done");

  // Locate the embedded brotli WASM blob in this file
  const sfFd = fs.openSync(vegPath, 'r');
  const wasmLen = "{{NODE_WASM_LEN}}";
  const wasmOff = "{{NODE_WASM_OFF}}";
  L("[starter] wasmOff:", wasmOff, "wasmLen:", wasmLen);
  const wasmBlob = Buffer.alloc(wasmLen);
  fs.readSync(sfFd, wasmBlob, 0, wasmLen, wasmOff);
  fs.closeSync(sfFd);
  L("[starter] wasm blob read done");

  L("[starter] instantiating wasm...");
  const { instance } = await WebAssembly.instantiate(wasmBlob, {});
  L("[starter] wasm instantiated, exports:", Object.keys(instance.exports));
  const { memory, brot_alloc, brot_decompress } = instance.exports;

  L("[starter] decompressing pool of len", compressedData.length, "...");
  const inPtr = brot_alloc(compressedData.length);
  new Uint8Array(memory.buffer, inPtr, compressedData.length).set(compressedData);

  const packed = brot_decompress(inPtr, compressedData.length);
  L("[starter] decompress done, packed result:", packed);
  if (packed === 0n) {
    console.error("[mohabbat] brotli decompression failed");
    process.exit(7);
  }

  const outPtr = Number(packed & 0xFFFFFFFFn);
  const outLen = Number(packed >> 32n);
  const pool = new Uint8Array(memory.buffer, outPtr, outLen);

  const washmhostOff = "{{NODE_WASHMHOST_OFF}}";
  const washmhostLen = "{{NODE_WASHMHOST_LEN}}";
  L("[starter] wasmhostOff:", washmhostOff, "wasmhostLen:", washmhostLen);
  const washmhostJs = pool.subarray(washmhostOff, washmhostOff + washmhostLen);

  const payloadOff = "{{NODE_PAYLOAD_OFF}}";
  const payloadLen = "{{NODE_PAYLOAD_LEN}}";
  L("[starter] payloadOff:", payloadOff, "payloadLen:", payloadLen);
  const brainWasm = pool.subarray(payloadOff, payloadOff + payloadLen);

  const hostCode = new TextDecoder().decode(washmhostJs);
  L("[starter] hostCode starts with:", hostCode.substring(0, 50));
  
  // Reconstruct host module
  // Using dynamic import of data URI to load the ES module
  const base64Code = Buffer.from(hostCode).toString('base64');
  L("[starter] base64Code len:", base64Code.length);
  const mod = await import('data:text/javascript;base64,' + base64Code);
  L("[starter] module imported");
  
  if (!mod.runHost) {
    console.error("[mohabbat] missing runHost in washmhost JS");
    process.exit(8);
  }
  
  await mod.runHost(brainWasm, process.argv.slice(2));
}

run().catch(err => {
  console.error(err);
  process.exit(1);
});
