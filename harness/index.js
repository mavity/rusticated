/**
 * rusticated terminal harness
 *
 * Scenario:
 *   1. Spawn each variant in a real ConPTY.
 *   2. When the prompt `> ` appears, type a few characters WITHOUT pressing Enter.
 *   3. The demo's own 5-second timer runs out; the demo prints "Timed out" and cleans up.
 *   4. If it has not exited within 25 seconds from spawn, it is forcibly killed.
 *
 * Output: harness-capture.md (fixed name, overwritten each run).
 * Format: one H1 section per variant, H2 sub-sections per phase with elapsed times.
 *
 * Usage:
 *   node harness/index.js [variants...]
 *   node harness/index.js native wasmtime node   (default: all three)
 *
 * Flags:
 *   --chars <text>       Characters to type at the prompt, no Enter (default: "hello")
 *   --kill-after <ms>    Forcible kill timeout per variant in ms (default: 25000)
 *   --out <file>         Override output path (default: harness-capture.md in repo root)
 *   --no-color           Disable ANSI colour in terminal live output
 *   --raw                Also print hex bytes of each PTY chunk to the terminal
 *   --cols <n>           PTY column width (default: 80)
 *   --rows <n>           PTY row height (default: 24)
 */

import pty from 'node-pty';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { existsSync, writeFileSync } from 'node:fs';
import process from 'node:process';

// ── paths ─────────────────────────────────────────────────────────────────────
const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const EXE = process.platform === 'win32' ? '.exe' : '';
const PATHS = {
  wasm:     path.join(ROOT, 'target/wasm32-unknown-unknown/debug/rusticated-demo.wasm'),
  native:   path.join(ROOT, `target/debug/rusticated-demo${EXE}`),
  wasmtime: path.join(ROOT, `target/debug/rusticated-wasmtime${EXE}`),
  nodeHost: path.join(ROOT, 'node-host/index.js'),
};

const ALL_VARIANTS = ['native', 'wasmtime', 'node'];

// ── CLI args ──────────────────────────────────────────────────────────────────
const args = process.argv.slice(2);
let charsToType = 'hello';
let killAfterMs = 25000;
let useColor    = true;
let showRaw     = false;
let cols        = 80;
let rows        = 24;
let outFile     = null;
const variantArgs = [];

for (let i = 0; i < args.length; i++) {
  switch (args[i]) {
    case '--chars':      charsToType = args[++i]; break;
    case '--kill-after': killAfterMs = Number(args[++i]); break;
    case '--out':        outFile     = args[++i]; break;
    case '--no-color':   useColor    = false; break;
    case '--raw':        showRaw     = true; break;
    case '--cols':       cols        = Number(args[++i]); break;
    case '--rows':       rows        = Number(args[++i]); break;
    default:
      if (!args[i].startsWith('--')) variantArgs.push(args[i]);
  }
}

const selectedVariants = variantArgs.length ? variantArgs : ALL_VARIANTS;
const captureFile = outFile ?? path.join(ROOT, 'harness-capture.md');

// Human-readable platform tag appended to native/wasmtime headings.
// WASM (node) is platform-neutral from the guest's perspective, so it gets no tag.
const OS_NAME = { win32: 'Windows', linux: 'Linux', darwin: 'macOS' }[process.platform] ?? process.platform;
const PLATFORM = `${OS_NAME} ${process.arch}`;
function variantTitle(name) {
  return (name === 'native' || name === 'wasmtime') ? `${name} (${PLATFORM})` : name;
}

// ── ANSI helpers ──────────────────────────────────────────────────────────────
const C = {
  reset:   useColor ? '\x1b[0m'  : '',
  bold:    useColor ? '\x1b[1m'  : '',
  red:     useColor ? '\x1b[31m' : '',
  green:   useColor ? '\x1b[32m' : '',
  cyan:    useColor ? '\x1b[36m' : '',
  magenta: useColor ? '\x1b[35m' : '',
  yellow:  useColor ? '\x1b[33m' : '',
  dim:     useColor ? '\x1b[2m'  : '',
};

const VARIANT_COLORS = { native: C.cyan, wasmtime: C.magenta, node: C.yellow };

// Strip ANSI/control sequences for plain-text file output
function stripAnsi(str) {
  return str
    .replace(/\x1b\[[0-9;]*[A-Za-z]/g, '')
    .replace(/\x1b\][^\x07]*\x07/g, '')
    .replace(/\r/g, '');
}

// Format elapsed milliseconds as 0.000s
function fmtElapsed(ms) {
  return (ms / 1000).toFixed(3) + 's';
}

// ── variant definitions ───────────────────────────────────────────────────────
function getVariantDef(name) {
  switch (name) {
    case 'native':   return { cmd: PATHS.native,      args: [] };
    case 'wasmtime': return { cmd: PATHS.wasmtime,    args: [PATHS.wasm] };
    case 'node':     return { cmd: process.execPath,  args: [PATHS.nodeHost, PATHS.wasm] };
    default: throw new Error(`Unknown variant: ${name}. Valid: ${ALL_VARIANTS.join(', ')}`);
  }
}

// ── preflight ─────────────────────────────────────────────────────────────────
function preflight(variants) {
  const missing = [];
  if (variants.includes('native') && !existsSync(PATHS.native))
    missing.push(`native exe: ${PATHS.native}`);
  if (variants.includes('wasmtime') && !existsSync(PATHS.wasmtime))
    missing.push(`wasmtime exe: ${PATHS.wasmtime}`);
  if ((variants.includes('wasmtime') || variants.includes('node')) && !existsSync(PATHS.wasm))
    missing.push(`wasm module: ${PATHS.wasm}`);
  if (variants.includes('node') && !existsSync(PATHS.nodeHost))
    missing.push(`node host: ${PATHS.nodeHost}`);
  if (missing.length) {
    console.error(`${C.red}${C.bold}Preflight failed:${C.reset}`);
    missing.forEach(m => console.error(`  ${C.red}x${C.reset} ${m}`));
    process.exit(1);
  }
}

// ── core runner ───────────────────────────────────────────────────────────────
/**
 * Runs one variant and returns an array of phases:
 *   { heading: string, lines: string[] }
 *
 * Phases (in order):
 *   starting <ISO timestamp>
 *   typing "<chars>" <elapsed>
 *   waited 5s, terminating <elapsed>
 *   [terminated forcefully <elapsed>]   -- only if force-killed
 *
 * The elapsed on each phase header is measured from the PREVIOUS phase header.
 */
function runVariant(name, { chars, killMs, verbose, raw: showHex }) {
  const def = getVariantDef(name);

  return new Promise((resolve) => {
    // ── phase tracking ────────────────────────────────────────────────────────
    const phases = [];
    let cur = null;        // current phase object { heading, chunks[] }
    let markerTs = 0;      // timestamp of last phase start
    let forcedKill = false;
    let charsSent = false;
    let timeoutDetected = false;
    let resolved = false;
    const t0 = Date.now();

    function newPhase(heading) {
      cur = { heading, chunks: [] };
      phases.push(cur);
      markerTs = Date.now();
    }

    function elapsedSince() {
      return fmtElapsed(Date.now() - markerTs);
    }

    // Phase 1 — output before typing
    newPhase(`starting ${new Date().toISOString()}`);

    // ── settle ────────────────────────────────────────────────────────────────
    function settle(exitCode) {
      if (resolved) return;
      resolved = true;
      clearTimeout(watchdog);
      const plain = phases.map(p => ({
        heading: p.heading,
        text: stripAnsi(p.chunks.join('')),
      }));
      resolve({ name, phases: plain, exitCode, forcedKill, durationMs: Date.now() - t0 });
    }

    // ── spawn ─────────────────────────────────────────────────────────────────
    const ptyProc = pty.spawn(def.cmd, def.args, {
      name: 'xterm-256color', cols, rows, cwd: ROOT, env: process.env,
    });

    // Hard-kill watchdog
    const watchdog = setTimeout(() => {
      forcedKill = true;
      const heading = `terminated forcefully ${elapsedSince()}`;
      newPhase(heading);
      if (verbose) process.stdout.write(`\r\n${C.red}${C.bold}## ${heading}${C.reset}\r\n`);
      try { ptyProc.kill(); } catch (_) { /* already gone */ }
      settle(null);
    }, killMs);

    // ── data handler ──────────────────────────────────────────────────────────
    ptyProc.onData((data) => {
      // Build the full plain-text view including this chunk
      const allPlain = stripAnsi(phases.flatMap(p => p.chunks).join('') + data);

      // Transition: "Timed out" detected -> "waited 5s, terminating" phase
      if (!timeoutDetected && charsSent && /timed out/i.test(allPlain)) {
        timeoutDetected = true;
        const heading = `waited 5s, terminating ${elapsedSince()}`;
        newPhase(heading);
        if (verbose) process.stdout.write(`\r\n${C.yellow}${C.bold}## ${heading}${C.reset}\r\n`);
      }

      // Accumulate into current phase
      if (cur) cur.chunks.push(data);

      // Live terminal output
      if (verbose) process.stdout.write(data);
      if (showHex) {
        const hex = Buffer.from(data).toString('hex').match(/.{1,2}/g).join(' ');
        process.stdout.write(`\n${C.dim}  hex: ${hex}${C.reset}\n`);
      }

      // Trigger: prompt seen -> transition to "typing" phase (with delay before sending)
      if (!charsSent && />\s*$/.test(allPlain)) {
        charsSent = true;
        const elapsed = elapsedSince(); // capture now, before the delay
        setTimeout(() => {
          if (resolved) return;
          const heading = `typing ${JSON.stringify(chars)} ${elapsed}`;
          newPhase(heading);
          if (verbose) process.stdout.write(`\r\n${C.green}${C.bold}## ${heading}${C.reset}\r\n`);
          // Send each character with a small gap
          let i = 0;
          function sendNext() {
            if (resolved || i >= chars.length) return;
            ptyProc.write(chars[i++]);
            if (i < chars.length) setTimeout(sendNext, 80);
          }
          sendNext();
        }, 200);
      }
    });

    ptyProc.onExit(({ exitCode }) => {
      // Add a closing phase line before settling
      const okFail = exitCode === 0 ? 'OK' : 'FAIL';
      const heading = `exited ${exitCode} ${okFail} ${elapsedSince()}`;
      newPhase(heading);
      const col = exitCode === 0 ? C.green : C.red;
      if (verbose) process.stdout.write(`\r\n${col}${C.bold}## ${heading}${C.reset}\r\n`);
      settle(exitCode);
    });
  });
}

// ── markdown writer ───────────────────────────────────────────────────────────
function buildMarkdown(allResults) {
  const sections = [];

  for (const r of allResults) {
    const lines = [`# ${variantTitle(r.name)}`, ''];

    for (const phase of r.phases) {
      lines.push(`## ${phase.heading}`, '');
      // Trim trailing blank lines from phase output, then append
      const text = phase.text.replace(/\n+$/, '');
      if (text) {
        lines.push(text, '');
      }
    }

    sections.push(lines.join('\n'));
  }

  return sections.join('\n---\n\n');
}

// ── main ──────────────────────────────────────────────────────────────────────
async function main() {
  preflight(selectedVariants);

  process.stdout.write(
    `${C.bold}rusticated harness${C.reset}\n` +
    `  variants: ${selectedVariants.join(', ')}\n` +
    `  typing:   ${JSON.stringify(charsToType)} (no Enter)\n` +
    `  kill at:  ${killAfterMs / 1000}s\n` +
    `  capture:  ${captureFile}\n\n`
  );

  const results = [];

  for (const name of selectedVariants) {
    const col = VARIANT_COLORS[name] ?? C.cyan;
    process.stdout.write(`\n${col}${C.bold}# ${variantTitle(name)}${C.reset}\n`);
    process.stdout.write(`${C.dim}## starting${C.reset}\n\n`);

    const result = await runVariant(name, {
      chars:   charsToType,
      killMs:  killAfterMs,
      verbose: true,
      raw:     showRaw,
    });

    results.push(result);
    process.stdout.write('\n');
  }

  // Write the capture file
  const md = buildMarkdown(results);
  writeFileSync(captureFile, md, 'utf8');

  // Terminal summary
  process.stdout.write(`\n${C.bold}Summary:${C.reset}\n`);
  for (const r of results) {
    const col = VARIANT_COLORS[r.name] ?? C.cyan;
    const status = r.forcedKill
      ? `${C.red}${C.bold}FORCIBLY TERMINATED${C.reset}`
      : r.exitCode === 0
        ? `${C.green}OK${C.reset}`
        : `${C.red}exit ${r.exitCode}${C.reset}`;
    process.stdout.write(`  ${col}${C.bold}${r.name.padEnd(10)}${C.reset}  ${status}  ${C.dim}${r.durationMs}ms${C.reset}\n`);
  }
  process.stdout.write(`\n${C.bold}Capture:${C.reset} ${captureFile}\n`);

  const anyBad = results.some(r => r.forcedKill || r.exitCode !== 0);
  process.exit(anyBad ? 1 : 0);
}

main().catch(e => {
  console.error(`${C.red}Fatal:${C.reset}`, e);
  process.exit(1);
});
