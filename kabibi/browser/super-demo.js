(function (global) {
  'use strict';

  const VIRTUAL_KEYS = [
    { label: 'Esc', ariaLabel: 'Escape', sequence: '\x1b' },
    { label: 'Tab', ariaLabel: 'Tab', sequence: '\t' },
    { label: 'F1', ariaLabel: 'F1', sequence: '\x1bOP' },
    { label: 'F2', ariaLabel: 'F2', sequence: '\x1bOQ' },
    { label: 'F3', ariaLabel: 'F3', sequence: '\x1bOR' },
    { label: 'F4', ariaLabel: 'F4', sequence: '\x1bOS' },
    { label: 'F5', ariaLabel: 'F5', sequence: '\x1b[15~' },
    { label: 'F6', ariaLabel: 'F6', sequence: '\x1b[17~' },
    { label: 'F7', ariaLabel: 'F7', sequence: '\x1b[18~' },
    { label: 'F8', ariaLabel: 'F8', sequence: '\x1b[19~' },
    { label: 'F9', ariaLabel: 'F9', sequence: '\x1b[20~' },
    { label: 'F10', ariaLabel: 'F10', sequence: '\x1b[21~' },
    { label: 'F11', ariaLabel: 'F11', sequence: '\x1b[23~' },
    { label: 'F12', ariaLabel: 'F12', sequence: '\x1b[24~' },
    { label: '←', ariaLabel: 'Arrow left', arrow: 'D' },
    { label: '↑', ariaLabel: 'Arrow up', arrow: 'A' },
    { label: '↓', ariaLabel: 'Arrow down', arrow: 'B' },
    { label: '→', ariaLabel: 'Arrow right', arrow: 'C' }
  ];

  function createVirtualKeyboard(document, terminal, root) {
    if (!terminal || typeof terminal.input !== 'function') {
      throw new Error('Super-demo screen keys require the xterm.js input API');
    }

    const style = document.createElement('style');
    style.textContent = `
      .kabibi-keyboard {
        box-sizing: border-box;
        flex: 0 0 auto;
        width: 100%;
        padding: clamp(4px, 0.8cqw, 10px) max(6px, env(safe-area-inset-right))
          max(6px, env(safe-area-inset-bottom)) max(6px, env(safe-area-inset-left));
        border-top: 1px solid #4b525a;
        background:
          linear-gradient(180deg, rgba(255,255,255,.07), transparent 12px),
          linear-gradient(180deg, #252a30 0%, #121519 46%, #080a0c 100%);
        box-shadow: 0 -5px 18px rgba(0,0,0,.62), inset 0 1px 0 rgba(255,255,255,.12);
        user-select: none;
        -webkit-user-select: none;
        touch-action: manipulation;
      }
      .kabibi-keyboard-grid {
        display: grid;
        grid-template-columns: 1.55fr repeat(18, minmax(0, 1fr));
        gap: clamp(2px, 0.42cqw, 7px);
        width: 100%;
      }
      .kabibi-keyboard--narrow .kabibi-keyboard-grid {
        grid-template-columns: 1.55fr repeat(8, minmax(0, 1fr));
      }
      .kabibi-key {
        position: relative;
        box-sizing: border-box;
        min-width: 0;
        height: clamp(2.25rem, 6dvh, 3.5rem);
        padding: 0 clamp(2px, 0.24cqw, 5px);
        overflow: hidden;
        border: 1px solid #080a0b;
        border-radius: 2px;
        color: #c4d2da;
        background:
          linear-gradient(180deg, #555d62 0%, #343a3e 12%, #202529 78%, #111518 100%);
        box-shadow:
          0 3px 0 #080a0b,
          0 4px 4px rgba(0,0,0,.72),
          inset 0 1px 0 rgba(255,255,255,.24),
          inset 1px 0 0 rgba(255,255,255,.06),
          inset 0 -2px 0 rgba(0,0,0,.7);
        font: 600 clamp(.62rem, 1.35cqw, .95rem)/1 system-ui, sans-serif;
        text-shadow: 0 -1px 1px #000, 0 1px 0 rgba(255,255,255,.12);
        cursor: pointer;
        -webkit-tap-highlight-color: transparent;
      }
      .kabibi-key::before {
        position: absolute;
        inset: 2px 3px auto;
        height: 1px;
        background: rgba(255,255,255,.15);
        content: '';
        pointer-events: none;
      }
      .kabibi-key--ctrl {
        display: flex;
        align-items: center;
        justify-content: center;
        gap: clamp(2px, 0.3cqw, 6px);
        color: #d6e0e5;
        letter-spacing: .03em;
      }
      .kabibi-key--ctrl img {
        box-sizing: border-box;
        width: clamp(1.05rem, 2cqw, 1.7rem);
        height: clamp(1.05rem, 2cqw, 1.7rem);
        padding: 2px;
        filter: drop-shadow(0 0 0.5px white) drop-shadow(2px 1px 3px rgba(255,255,255,0.3));
        transform: translateX(-9%) scale(1.5);
        object-fit: contain;
        pointer-events: none;
      }
      .kabibi-key--ctrl span {
        font-size: clamp(.58rem, 1.1cqw, .78rem);
      }
      .kabibi-key--ctrl[aria-pressed="true"] {
        border-color: #9ab0bb;
        color: #f0c66c;
        background: linear-gradient(180deg, #41494e 0%, #252b2f 80%, #161a1d 100%);
        box-shadow:
          0 1px 0 #080a0b,
          inset 0 2px 4px rgba(0,0,0,.75),
          inset 0 1px 0 rgba(255,255,255,.12);
        transform: translateY(2px);
      }
      .kabibi-key:active {
        transform: translateY(2px);
        background: linear-gradient(180deg, #30363a 0%, #191e21 100%);
        box-shadow:
          0 1px 0 #080a0b,
          inset 0 2px 4px rgba(0,0,0,.72);
      }
      @supports not (height: 1dvh) {
        .kabibi-key { height: clamp(2.25rem, 6vh, 3.5rem); }
      }
      @supports not (width: 1cqw) {
        .kabibi-keyboard {
          padding: clamp(4px, 0.8vw, 10px) max(6px, env(safe-area-inset-right))
            max(6px, env(safe-area-inset-bottom)) max(6px, env(safe-area-inset-left));
        }
        .kabibi-keyboard-grid { gap: clamp(2px, 0.42vw, 7px); }
        .kabibi-key {
          font-size: clamp(.62rem, 1.35vw, .95rem);
        }
        .kabibi-key--ctrl img { width: clamp(1.05rem, 2vw, 1.7rem); height: clamp(1.05rem, 2vw, 1.7rem); }
        .kabibi-key--ctrl span { font-size: clamp(.58rem, 1.1vw, .78rem); }
      }
    `;
    const controls = document.createElement('div');
    controls.className = 'kabibi-keyboard';
    controls.setAttribute('role', 'group');
    controls.setAttribute('aria-label', 'Kabibi screen keyboard');
    const grid = document.createElement('div');
    grid.className = 'kabibi-keyboard-grid';
    controls.appendChild(grid);

    let controlActive = false;
    const controlButton = document.createElement('button');
    controlButton.type = 'button';
    controlButton.className = 'kabibi-key kabibi-key--ctrl';
    controlButton.setAttribute('aria-label', 'Ctrl');
    controlButton.setAttribute('aria-pressed', 'false');
    controlButton.title = 'Toggle Ctrl modifier';
    const logo = document.createElement('img');
    const favicon = typeof document.querySelector === 'function'
      ? document.querySelector('link[rel="icon"][type="image/svg+xml"]')
      : null;
    logo.src = favicon && favicon.href ? favicon.href : 'kabibi/kabibi.svg';
    logo.alt = '';
    logo.setAttribute('aria-hidden', 'true');
    logo.draggable = false;
    const controlLabel = document.createElement('span');
    controlLabel.textContent = 'Ctrl';
    controlButton.appendChild(logo);
    controlButton.appendChild(controlLabel);
    controlButton.addEventListener('pointerdown', (event) => event.preventDefault());
    controlButton.addEventListener('contextmenu', (event) => event.preventDefault());
    controlButton.addEventListener('click', (event) => {
      event.preventDefault();
      controlActive = !controlActive;
      controlButton.setAttribute('aria-pressed', String(controlActive));
    });
    grid.appendChild(controlButton);

    for (const key of VIRTUAL_KEYS) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'kabibi-key';
      button.textContent = key.label;
      button.setAttribute('aria-label', key.ariaLabel);
      button.title = key.ariaLabel;
      button.addEventListener('pointerdown', (event) => event.preventDefault());
      button.addEventListener('contextmenu', (event) => event.preventDefault());
      button.addEventListener('click', (event) => {
        event.preventDefault();
        let sequence = key.sequence;
        if (key.arrow) {
          const applicationMode = Boolean(
            terminal.modes && terminal.modes.applicationCursorKeys
          );
          sequence = `\x1b${applicationMode ? 'O' : '['}${key.arrow}`;
        }
        if (controlActive) {
          sequence = controlSequenceForKey(key, sequence);
        }
        terminal.input(sequence, true);
      });
      grid.appendChild(button);
    }

    root.appendChild(style);
    root.appendChild(controls);
    return {
      element: controls,
      setNarrow(narrow) {
        controls.className = `kabibi-keyboard${narrow ? ' kabibi-keyboard--narrow' : ''}`;
      },
      handleKeyEvent(event) {
        if (!controlActive || !event || event.type !== 'keydown' ||
            event.ctrlKey || event.metaKey || event.altKey || event.isComposing) {
          return true;
        }
        const sequence = controlSequenceForPhysicalKey(event.key);
        if (sequence === null) return true;
        event.preventDefault();
        if (typeof event.stopPropagation === 'function') event.stopPropagation();
        terminal.input(sequence, true);
        return false;
      },
      isControlActive() {
        return controlActive;
      },
      dispose() {
        controlActive = false;
        controlButton.setAttribute('aria-pressed', 'false');
        controls.remove();
        style.remove();
      }
    };
  }

  function controlSequenceForKey(key, sequence) {
    if (key.arrow) return `\x1b[1;5${key.arrow}`;
    if (key.label === 'Tab') return '\t';
    if (key.label === 'Esc') return '\x1b';
    const functionKey = /^F(\d+)$/.exec(key.label);
    if (functionKey) return modifiedFunctionKeySequence(Number(functionKey[1]));
    return controlSequenceForPhysicalKey(key.label);
  }

  function controlSequenceForPhysicalKey(key) {
    if (key === ' ') return '\x00';
    if (typeof key !== 'string' || key.length !== 1) {
      const specialKeys = {
        Backspace: '\x7f',
        Enter: '\r',
        Tab: '\t',
        Escape: '\x1b',
        ArrowUp: '\x1b[1;5A',
        ArrowDown: '\x1b[1;5B',
        ArrowRight: '\x1b[1;5C',
        ArrowLeft: '\x1b[1;5D',
        Home: '\x1b[1;5H',
        End: '\x1b[1;5F',
        Insert: '\x1b[2;5~',
        Delete: '\x1b[3;5~',
        PageUp: '\x1b[5;5~',
        PageDown: '\x1b[6;5~'
      };
      if (specialKeys[key]) return specialKeys[key];
      const functionKey = /^F(\d+)$/.exec(key);
      return functionKey ? modifiedFunctionKeySequence(Number(functionKey[1])) : null;
    }

    const digitControls = { '2': '\x00', '3': '\x1b', '4': '\x1c', '5': '\x1d', '6': '\x1e', '7': '\x1f', '8': '\x7f' };
    if (digitControls[key]) return digitControls[key];
    const normalized = key.toUpperCase();
    if (normalized >= '@' && normalized <= '_') {
      return String.fromCharCode(normalized.charCodeAt(0) & 0x1f);
    }
    if (key === '?') return '\x7f';
    return null;
  }

  function modifiedFunctionKeySequence(number) {
    const functionFinals = { 1: 'P', 2: 'Q', 3: 'R', 4: 'S' };
    if (functionFinals[number]) return `\x1b[1;5${functionFinals[number]}`;
    const functionNumbers = { 5: 15, 6: 17, 7: 18, 8: 19, 9: 20, 10: 21, 11: 23, 12: 24 };
    return functionNumbers[number] ? `\x1b[${functionNumbers[number]};5~` : null;
  }

  const CRT_CURVE_KZ = 0.24;
  const CRT_CURVE_AY = 0.72;
  const GLASS_IOR_R = 1.506;
  const GLASS_IOR_G = 1.520;
  const GLASS_IOR_B = 1.538;
  const GLASS_F0 = Math.pow((GLASS_IOR_G - 1) / (GLASS_IOR_G + 1), 2);
  const GLASS_BASE_THICKNESS = 0.036;
  const GLASS_CORNER_THICKENING = 0.24;
  const BASE_LIGHT_X = -1.7;
  const BASE_LIGHT_Y = 2.18;
  const BASE_LIGHT_DEPTH = 2.2;
  const LIGHT_SPHERE_RADIUS = 0.55;
  const LIGHT_ELLIPSOID_RADII = Object.freeze({ x: 1.35, y: 4.50, z: 1.20 });
  const LIGHT_ELLIPSOID_OFFSET = Object.freeze({ x: 0.52, y: -1.35, z: 0.12 });

  function srgbChannelToLinear(c) {
    return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  }

  function parseHexColorLinear(hex, fallback = [0.002, 0.003, 0.006]) {
    if (typeof hex !== 'string') return fallback;
    const cleaned = hex.trim().replace(/^#/, '');
    if (cleaned.length !== 6) return fallback;
    const num = Number.parseInt(cleaned, 16);
    if (!Number.isFinite(num)) return fallback;
    const r = ((num >> 16) & 0xff) / 255;
    const g = ((num >> 8) & 0xff) / 255;
    const b = (num & 0xff) / 255;
    return [srgbChannelToLinear(r), srgbChannelToLinear(g), srgbChannelToLinear(b)];
  }

  function mat4Perspective(out, fovDeg, aspect, near, far) {
    const f = 1.0 / Math.tan((fovDeg * Math.PI) / 360.0);
    const nf = 1.0 / (near - far);
    out.fill(0);
    out[0] = f / (aspect || 1);
    out[5] = f;
    out[10] = (far + near) * nf;
    out[11] = -1.0;
    out[14] = 2.0 * far * near * nf;
    return out;
  }

  function mat4InvertPerspective(out, proj) {
    out.fill(0);
    out[0] = proj[0] !== 0 ? 1.0 / proj[0] : 0;
    out[5] = proj[5] !== 0 ? 1.0 / proj[5] : 0;
    out[11] = proj[14] !== 0 ? 1.0 / proj[14] : 0;
    out[14] = -1.0;
    out[15] = proj[14] !== 0 ? proj[10] / proj[14] : 0;
    return out;
  }

  function mat4LookAt(out, eye, target, up, cameraUpOut) {
    let zx = eye.x - target.x;
    let zy = eye.y - target.y;
    let zz = eye.z - target.z;
    const zLen = Math.hypot(zx, zy, zz) || 1;
    zx /= zLen;
    zy /= zLen;
    zz /= zLen;

    let xx = up.y * zz - up.z * zy;
    let xy = up.z * zx - up.x * zz;
    let xz = up.x * zy - up.y * zx;
    const xLen = Math.hypot(xx, xy, xz) || 1;
    xx /= xLen;
    xy /= xLen;
    xz /= xLen;

    const yx = zy * xz - zz * xy;
    const yy = zz * xx - zx * xz;
    const yz = zx * xy - zy * xx;
    if (cameraUpOut) {
      cameraUpOut.x = yx;
      cameraUpOut.y = yy;
      cameraUpOut.z = yz;
    }

    out[0] = xx;
    out[1] = yx;
    out[2] = zx;
    out[3] = 0;
    out[4] = xy;
    out[5] = yy;
    out[6] = zz;
    out[7] = 0;
    out[8] = xz;
    out[9] = yz;
    out[10] = zz;
    out[11] = 0;
    out[12] = -(xx * eye.x + xy * eye.y + xz * eye.z);
    out[13] = -(yx * eye.x + yy * eye.y + yz * eye.z);
    out[14] = -(zx * eye.x + zy * eye.y + zz * eye.z);
    out[15] = 1;
    // Fix column 1 row 2 (out[6] is zy)
    out[6] = zy;
    return out;
  }

  function mat4Multiply(out, a, b) {
    const a00 = a[0], a01 = a[1], a02 = a[2], a03 = a[3];
    const a10 = a[4], a11 = a[5], a12 = a[6], a13 = a[7];
    const a20 = a[8], a21 = a[9], a22 = a[10], a23 = a[11];
    const a30 = a[12], a31 = a[13], a32 = a[14], a33 = a[15];
    for (let col = 0; col < 4; col += 1) {
      const idx = col * 4;
      const b0 = b[idx], b1 = b[idx + 1], b2 = b[idx + 2], b3 = b[idx + 3];
      out[idx] = a00 * b0 + a10 * b1 + a20 * b2 + a30 * b3;
      out[idx + 1] = a01 * b0 + a11 * b1 + a21 * b2 + a31 * b3;
      out[idx + 2] = a02 * b0 + a12 * b1 + a22 * b2 + a32 * b3;
      out[idx + 3] = a03 * b0 + a13 * b1 + a23 * b2 + a33 * b3;
    }
    return out;
  }

  function refract3D(ix, iy, iz, nx, ny, nz, eta) {
    const dotIN = ix * nx + iy * ny + iz * nz;
    const k = 1.0 - eta * eta * (1.0 - dotIN * dotIN);
    if (k < 0.0) return { x: 0, y: 0, z: 0 };
    const factor = eta * dotIN + Math.sqrt(k);
    return {
      x: eta * ix - factor * nx,
      y: eta * iy - factor * ny,
      z: eta * iz - factor * nz
    };
  }

  function solveParaboloidRayHit(origin, dir, screenWidth, screenHeight, flat = false) {
    if (!origin || !dir || !(screenWidth > 0) || !(screenHeight > 0)) return null;
    const dLen = Math.hypot(dir.x, dir.y, dir.z) || 1;
    const dx = dir.x / dLen;
    const dy = dir.y / dLen;
    const dz = dir.z / dLen;

    let t = -1;
    if (flat) {
      if (Math.abs(dz) < 1e-8) return null;
      t = -origin.z / dz;
    } else {
      const ax = (4.0 * CRT_CURVE_KZ) / (screenWidth * screenWidth);
      const ay = (4.0 * CRT_CURVE_KZ * CRT_CURVE_AY) / (screenHeight * screenHeight);
      const A = ax * dx * dx + ay * dy * dy;
      const B = dz + 2.0 * (ax * origin.x * dx + ay * origin.y * dy);
      const C = origin.z + ax * origin.x * origin.x + ay * origin.y * origin.y;
      if (Math.abs(A) < 1e-10) {
        if (Math.abs(B) < 1e-10) return null;
        t = -C / B;
      } else {
        const disc = B * B - 4.0 * A * C;
        if (disc < 0) return null;
        const sqrtDisc = Math.sqrt(disc);
        const t0 = (-B - sqrtDisc) / (2.0 * A);
        const t1 = (-B + sqrtDisc) / (2.0 * A);
        t = t0 > 1e-5 ? t0 : (t1 > 1e-5 ? t1 : -1);
      }
    }
    if (!(t > 1e-5)) return null;
    const x = origin.x + t * dx;
    const y = origin.y + t * dy;
    const z = origin.z + t * dz;
    const u = x / screenWidth + 0.5;
    const v = y / screenHeight + 0.5;
    if (u < 0 || u > 1 || v < 0 || v > 1) return null;
    return { t, x, y, z, u, v };
  }

  function calculateLightPosition(elapsedMs, screenWidth = 2.72) {
    const offset = calculateReflectionOffset(elapsedMs);
    const depthScale = calculateReflectionDepth(elapsedMs);
    // Normalize swing from left (-1.2 -> sRight = 0) to right (+1.2 -> sRight = 1)
    // On the left, the light rig stays near BASE_LIGHT_DEPTH (~2.2); on the right, it recedes visibly farther.
    const sRight = Math.max(0, Math.min(1, (offset + 1.2) / 2.4));
    const sphereX = BASE_LIGHT_X + offset * screenWidth;
    const sphereY = BASE_LIGHT_Y;
    const sphereZ = BASE_LIGHT_DEPTH * (0.95 + 1.55 * sRight * sRight);
    return {
      x: sphereX,
      y: sphereY,
      z: sphereZ,
      radius: LIGHT_SPHERE_RADIUS,
      depthScale,
      ellipsoid: {
        x: sphereX + LIGHT_ELLIPSOID_OFFSET.x * (0.85 + 0.30 * (1 - sRight)),
        y: sphereY + LIGHT_ELLIPSOID_OFFSET.y,
        z: (sphereZ + LIGHT_ELLIPSOID_OFFSET.z) * 1.4,
        rx: LIGHT_ELLIPSOID_RADII.x,
        ry: LIGHT_ELLIPSOID_RADII.y,
        rz: LIGHT_ELLIPSOID_RADII.z
      }
    };
  }

  function refractThickGlassUV(uv, eye, screenWidth, screenHeight, flat = false) {
    const u = Math.max(0, Math.min(1, uv && Number.isFinite(uv.x) ? uv.x : 0.5));
    const v = Math.max(0, Math.min(1, uv && Number.isFinite(uv.y) ? uv.y : 0.5));
    if (flat) {
      return {
        uvR: { x: u, y: v },
        uvG: { x: u, y: v },
        uvB: { x: u, y: v },
        deltaUvG: { x: 0, y: 0 },
        bounceUv: { x: u, y: v },
        lensDensity: 1,
        fresnelOuter: GLASS_F0
      };
    }

    const nx = u * 2.0 - 1.0;
    const ny = v * 2.0 - 1.0;
    const px = nx * (screenWidth * 0.5);
    const py = ny * (screenHeight * 0.5);
    const r2 = nx * nx + CRT_CURVE_AY * ny * ny;
    const pz = -CRT_CURVE_KZ * r2;

    let normX = (4.0 * CRT_CURVE_KZ * nx) / screenWidth;
    let normY = (4.0 * CRT_CURVE_KZ * CRT_CURVE_AY * ny) / screenHeight;
    let normZ = 1.0;
    const normLen = Math.hypot(normX, normY, normZ) || 1;
    normX /= normLen;
    normY /= normLen;
    normZ /= normLen;

    let ix = px - eye.x;
    let iy = py - eye.y;
    let iz = pz - eye.z;
    const iLen = Math.hypot(ix, iy, iz) || 1;
    ix /= iLen;
    iy /= iLen;
    iz /= iLen;

    const ndotv = Math.max(0.001, -(ix * normX + iy * normY + iz * normZ));
    const refractedG = refract3D(ix, iy, iz, normX, normY, normZ, 1.0 / GLASS_IOR_G);
    const cosThetaT = Math.max(0.25, -(refractedG.x * normX + refractedG.y * normY + refractedG.z * normZ));
    const thickness = GLASS_BASE_THICKNESS * (1.0 + GLASS_CORNER_THICKENING * r2);
    const stepLen = thickness / cosThetaT;

    // Measure refraction offset in the local shell tangent frame relative to -N
    const tangentX = refractedG.x + cosThetaT * normX;
    const tangentY = refractedG.y + cosThetaT * normY;
    const deltaUvX = (tangentX * stepLen) / screenWidth;
    const deltaUvY = (tangentY * stepLen) / screenHeight;

    const grazing = 1.0 - ndotv * ndotv;
    const dispStrength = 0.028 + 0.045 * grazing;
    const uvG = { x: u + deltaUvX, y: v + deltaUvY };
    const uvR = {
      x: u + deltaUvX * (1.0 - dispStrength),
      y: v + deltaUvY * (1.0 - dispStrength)
    };
    const uvB = {
      x: u + deltaUvX * (1.0 + 1.18 * dispStrength),
      y: v + deltaUvY * (1.0 + 1.18 * dispStrength)
    };

    // Outward reflection direction relative to the camera's perpendicular/apex point on the curved screen:
    // Because the camera views from low-right, the zero-shift apex sits near u=0.60, v=0.42 (0.6/0.6 from top-left),
    // where normX_apex ≈ +0.070 and normY_apex ≈ -0.068. Measuring purely relative to that apex guarantees
    // the reflection is strictly leftward for all u < 0.60 (and ~1.6x stronger on the left edge than the right edge).
    const apexNormX = normX - 0.070;
    const apexNormY = normY + 0.068;
    const apexShiftX = (1.15 * apexNormX * thickness * 0.80) / screenWidth;
    const apexShiftY = (0.55 * apexNormY * thickness * 0.80) / screenHeight;
    const bounceUv = {
      x: uvG.x - apexShiftX,
      y: uvG.y - apexShiftY
    };

    const lensDensity = 1.0 + 0.28 * (1.0 - 0.65 * r2) * (cosThetaT / Math.max(ndotv, 0.22) - 1.0);
    const fresnelOuter = GLASS_F0 + (1.0 - GLASS_F0) * Math.pow(1.0 - ndotv, 5.0);

    return {
      uvR,
      uvG,
      uvB,
      deltaUvG: { x: deltaUvX, y: deltaUvY },
      bounceUv,
      lensDensity,
      fresnelOuter
    };
  }

  function calculateFocalBounds(eye, cameraUp, viewMatrix, screenWidth, screenHeight, flat = false) {
    const surfaceZ = (nx, ny) => (flat ? 0 : -CRT_CURVE_KZ * (nx * nx + CRT_CURVE_AY * ny * ny));
    const cornerPoint = {
      x: screenWidth * 0.5,
      y: -screenHeight * 0.5,
      z: surfaceZ(1, -1)
    };
    const focalHeight =
      (eye.x - cornerPoint.x) * cameraUp.x +
      (eye.y - cornerPoint.y) * cameraUp.y +
      (eye.z - cornerPoint.z) * cameraUp.z;
    const focalPoint = {
      x: cornerPoint.x + cameraUp.x * focalHeight,
      y: cornerPoint.y + cameraUp.y * focalHeight,
      z: cornerPoint.z + cameraUp.z * focalHeight
    };
    const focalPointView = {
      x: viewMatrix[0] * focalPoint.x + viewMatrix[4] * focalPoint.y + viewMatrix[8] * focalPoint.z + viewMatrix[12],
      y: viewMatrix[1] * focalPoint.x + viewMatrix[5] * focalPoint.y + viewMatrix[9] * focalPoint.z + viewMatrix[13],
      z: viewMatrix[2] * focalPoint.x + viewMatrix[6] * focalPoint.y + viewMatrix[10] * focalPoint.z + viewMatrix[14]
    };

    let nearestDistance = Infinity;
    let farthestDistance = -Infinity;
    const cols = 48;
    const rows = 28;
    for (let iy = 0; iy <= rows; iy += 1) {
      const ny = (iy / rows) * 2.0 - 1.0;
      const py = ny * (screenHeight * 0.5);
      for (let ix = 0; ix <= cols; ix += 1) {
        const nx = (ix / cols) * 2.0 - 1.0;
        const px = nx * (screenWidth * 0.5);
        const pz = surfaceZ(nx, ny);
        const dist = Math.hypot(px - focalPoint.x, py - focalPoint.y, pz - focalPoint.z);
        if (dist < nearestDistance) nearestDistance = dist;
        if (dist > farthestDistance) farthestDistance = dist;
      }
    }
    // Fine local refinement around the bottom-right region for nearestDistance
    for (let iy = 0; iy <= 16; iy += 1) {
      const ny = -1.0 + (iy / 16) * 0.5;
      const py = ny * (screenHeight * 0.5);
      for (let ix = 0; ix <= 16; ix += 1) {
        const nx = 0.5 + (ix / 16) * 0.5;
        const px = nx * (screenWidth * 0.5);
        const pz = surfaceZ(nx, ny);
        const dist = Math.hypot(px - focalPoint.x, py - focalPoint.y, pz - focalPoint.z);
        if (dist < nearestDistance) nearestDistance = dist;
      }
    }

    return {
      focalPoint,
      focalPointView,
      nearestDistance,
      farthestDistance
    };
  }

  function start({ terminal, fitAddon, root, terminalContainer, dependencies = {} }) {
    const WebglAddon = dependencies.WebglAddon;
    const document = dependencies.document || global.document;
    const flat = dependencies.flat === true;
    const upscaleFactor = 2;
    const scanlinePeriod = 4;
    if (!WebglAddon) {
      throw new Error('Super-demo mode requires the xterm.js WebGL addon');
    }

    const stage = document.createElement('div');
    stage.className = 'kabibi-crt-stage';
    Object.assign(stage.style, {
      position: 'relative',
      flex: '1 1 0',
      width: '100%',
      minHeight: '0',
      overflow: 'hidden',
      background: 'radial-gradient(ellipse at 50% 45%, #17202d 0%, #07090e 68%)',
      touchAction: 'none'
    });

    const canvas = document.createElement('canvas');
    canvas.setAttribute('aria-label', 'Kabibi terminal display');
    canvas.setAttribute('role', 'img');
    Object.assign(canvas.style, {
      display: 'block',
      width: '100%',
      height: '100%',
      touchAction: 'none'
    });
    stage.appendChild(canvas);
    Object.assign(root.style, {
      display: 'flex',
      flexDirection: 'column',
      minHeight: '0',
      containerType: 'inline-size'
    });
    Object.assign(terminalContainer.style, {
      position: 'absolute',
      inset: '0',
      width: '100%',
      height: '100%'
    });
    root.appendChild(stage);
    const keyboard = createVirtualKeyboard(document, terminal, root);

    let addon = null;
    let gl = null;
    let animationFrame = 0;
    let active = true;
    let usingFallback = false;
    let screenNeedsUpdate = true;
    let reflectionAnimationStart = null;
    let renderSubscription = null;
    let contextLossSubscription = null;
    let resizeObserver = null;
    let screenCanvas = null;
    let textureCanvas = null;
    let textureContext = null;

    let screenTex = null;
    let sceneFbo = null;
    let sceneColorTex = null;
    let sceneDepthTex = null;
    let blurFbo = null;
    let blurColorTex = null;
    let crtVao = null;
    let crtVbo = null;
    let crtIbo = null;
    let crtIndexCount = 0;
    let quadVao = null;
    let quadVbo = null;
    let crtProgram = null;
    let crtUniforms = null;
    let blurHProgram = null;
    let blurHUniforms = null;
    let blurVProgram = null;
    let blurVUniforms = null;
    let fboWidth = 0;
    let fboHeight = 0;
    let screenTexWidth = 0;
    let screenTexHeight = 0;

    const baseScreenWidth = 2.72;
    const baseScreenHeight = 1.58;
    let screenWidth = baseScreenWidth;
    let screenHeight = baseScreenHeight;
    const cameraFov = 31;
    const cameraNear = 0.1;
    const cameraFar = 50;
    let cameraAspect = 1.6;
    const cameraTarget = { x: 0.015, y: -0.015, z: 0 };
    const cameraOffset = { x: 0.295, y: -0.205, z: 2.66 };
    const cameraOffsetLength = Math.hypot(cameraOffset.x, cameraOffset.y, cameraOffset.z);
    const cameraEye = { x: 0.31, y: -0.22, z: 2.66 };
    const cameraLookTarget = { x: 0.015, y: -0.015, z: 0 };
    const cameraUp = { x: 0, y: 1, z: 0 };
    const worldUp = { x: 0, y: 1, z: 0 };

    const projMatrix = new Float32Array(16);
    const projMatrixInverse = new Float32Array(16);
    const viewMatrix = new Float32Array(16);
    const viewProjMatrix = new Float32Array(16);
    const imageScale = { x: 1, y: 1 };
    const screenResolution = { x: 1, y: 1 };
    const postResolution = { x: 1, y: 1 };
    const focalPointView = { x: 0, y: 0, z: 0 };
    let nearestDistance = 0;
    let farthestDistance = 1;
    const maxBlurPixels = 8.0;
    const opticalEffects = flat ? 0.0 : 1.0;
    const terminalBackgroundRgb = parseHexColorLinear(
      (terminal.options && terminal.options.theme && terminal.options.theme.background) || '#07090e'
    );
    const screenBackgroundRgb = parseHexColorLinear('#101923');

    let dragging = false;
    const terminalScreen = typeof terminal.element.querySelector === 'function'
      ? terminal.element.querySelector('.xterm-screen') || terminal.element
      : terminal.element;
    const initialTerminalSize = { cols: terminal.cols, rows: terminal.rows };

    function fitTerminal() {
      if (!active || usingFallback) return false;
      const bounds = stage.getBoundingClientRect();
      const dimensions = calculateGrid(bounds.width || root.clientWidth, bounds.height || root.clientHeight);
      if (terminal.cols !== dimensions.cols || terminal.rows !== dimensions.rows) {
        terminal.resize(dimensions.cols, dimensions.rows);
      }
      return true;
    }

    function restoreTerminalSize() {
      const previousSize = { cols: terminal.cols, rows: terminal.rows };
      if (fitAddon && typeof fitAddon.fit === 'function') {
        fitAddon.fit();
      }
      if (terminal.cols === previousSize.cols && terminal.rows === previousSize.rows) {
        terminal.resize(initialTerminalSize.cols, initialTerminalSize.rows);
      }
    }

    terminalContainer.style.opacity = '0';
    terminalContainer.style.pointerEvents = 'none';

    function disposeScene() {
      if (animationFrame) cancelAnimationFrame(animationFrame);
      animationFrame = 0;
      if (renderSubscription) renderSubscription.dispose();
      renderSubscription = null;
      if (contextLossSubscription) contextLossSubscription.dispose();
      contextLossSubscription = null;
      if (resizeObserver) resizeObserver.disconnect();
      resizeObserver = null;
      if (gl) {
        if (screenTex && typeof gl.deleteTexture === 'function') gl.deleteTexture(screenTex);
        if (sceneColorTex && typeof gl.deleteTexture === 'function') gl.deleteTexture(sceneColorTex);
        if (sceneDepthTex && typeof gl.deleteTexture === 'function') gl.deleteTexture(sceneDepthTex);
        if (blurColorTex && typeof gl.deleteTexture === 'function') gl.deleteTexture(blurColorTex);
        if (sceneFbo && typeof gl.deleteFramebuffer === 'function') gl.deleteFramebuffer(sceneFbo);
        if (blurFbo && typeof gl.deleteFramebuffer === 'function') gl.deleteFramebuffer(blurFbo);
        if (crtVao && typeof gl.deleteVertexArray === 'function') gl.deleteVertexArray(crtVao);
        if (crtVbo && typeof gl.deleteBuffer === 'function') gl.deleteBuffer(crtVbo);
        if (crtIbo && typeof gl.deleteBuffer === 'function') gl.deleteBuffer(crtIbo);
        if (quadVao && typeof gl.deleteVertexArray === 'function') gl.deleteVertexArray(quadVao);
        if (quadVbo && typeof gl.deleteBuffer === 'function') gl.deleteBuffer(quadVbo);
        if (crtProgram && typeof gl.deleteProgram === 'function') gl.deleteProgram(crtProgram);
        if (blurHProgram && typeof gl.deleteProgram === 'function') gl.deleteProgram(blurHProgram);
        if (blurVProgram && typeof gl.deleteProgram === 'function') gl.deleteProgram(blurVProgram);
      }
      screenTex = null;
      sceneColorTex = null;
      sceneDepthTex = null;
      blurColorTex = null;
      sceneFbo = null;
      blurFbo = null;
      crtVao = null;
      crtVbo = null;
      crtIbo = null;
      quadVao = null;
      quadVbo = null;
      crtProgram = null;
      crtUniforms = null;
      blurHProgram = null;
      blurHUniforms = null;
      blurVProgram = null;
      blurVUniforms = null;
      gl = null;
      screenCanvas = null;
      textureCanvas = null;
      textureContext = null;
    }

    function onWebGLContextLost(event) {
      if (event && typeof event.preventDefault === 'function') event.preventDefault();
      fallback(new Error('CRT display WebGL2 context was lost'));
    }

    function removeInputListeners() {
      canvas.removeEventListener('mousemove', onCanvasMove);
      canvas.removeEventListener('mousedown', onCanvasDown);
      canvas.removeEventListener('click', forwardMouseEvent);
      canvas.removeEventListener('dblclick', forwardMouseEvent);
      canvas.removeEventListener('wheel', forwardMouseEvent);
      canvas.removeEventListener('contextmenu', forwardMouseEvent);
      canvas.removeEventListener('webglcontextlost', onWebGLContextLost);
      global.removeEventListener('mousemove', onWindowMove);
      global.removeEventListener('mouseup', onWindowUp);
      dragging = false;
    }

    function fallback(reason) {
      if (!active || usingFallback) return;
      usingFallback = true;
      removeInputListeners();
      disposeScene();
      if (addon) addon.dispose();
      addon = null;
      stage.remove();
      keyboard.dispose();
      terminalContainer.style.opacity = '';
      terminalContainer.style.pointerEvents = '';
      restoreTerminalSize();
      if (reason) {
        console.warn(`Kabibi super-demo is using direct xterm rendering: ${reason.message || reason}`);
      }
    }

    function requestFrame(refreshScreen = false) {
      if (refreshScreen) screenNeedsUpdate = true;
      if (!active || usingFallback || animationFrame) return;
      animationFrame = requestAnimationFrame(render);
    }

    function getScreenViewport() {
      const renderService = terminal._core && terminal._core._renderService;
      const deviceCanvas = renderService && renderService.dimensions &&
        renderService.dimensions.device && renderService.dimensions.device.canvas;
      return calculateCaptureViewport(
        screenCanvas.width,
        screenCanvas.height,
        deviceCanvas && deviceCanvas.width,
        deviceCanvas && deviceCanvas.height
      );
    }

    function drawWarmTerminalImage(viewport, targetWidth, targetHeight) {
      textureContext.save();
      textureContext.filter = 'sepia(40%) saturate(1.05)';
      textureContext.drawImage(
        screenCanvas,
        viewport.x,
        viewport.y,
        viewport.width,
        viewport.height,
        0,
        0,
        targetWidth,
        targetHeight
      );
      textureContext.restore();
    }

    function uploadScreenTexture(resized) {
      if (!gl || !textureCanvas) return;
      if (!screenTex) {
        screenTex = gl.createTexture();
      }
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, screenTex);
      gl.pixelStorei(gl.UNPACK_FLIP_Y_WEBGL, true);
      if (resized || screenTexWidth !== textureCanvas.width || screenTexHeight !== textureCanvas.height) {
        screenTexWidth = textureCanvas.width;
        screenTexHeight = textureCanvas.height;
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
        gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
        gl.texImage2D(
          gl.TEXTURE_2D,
          0,
          gl.SRGB8_ALPHA8,
          gl.RGBA,
          gl.UNSIGNED_BYTE,
          textureCanvas
        );
      } else {
        gl.texSubImage2D(
          gl.TEXTURE_2D,
          0,
          0,
          0,
          gl.RGBA,
          gl.UNSIGNED_BYTE,
          textureCanvas
        );
      }
    }

    function updateScreenTexture() {
      const latestCanvas = addon && addon._renderer && addon._renderer._canvas;
      if (latestCanvas) screenCanvas = latestCanvas;
      if (!screenCanvas || screenCanvas.width <= 0 || screenCanvas.height <= 0 ||
          !textureCanvas || !textureContext) return;

      const viewport = getScreenViewport();
      const targetWidth = viewport.width * upscaleFactor;
      const targetHeight = viewport.height * upscaleFactor;
      const resized = textureCanvas.width !== targetWidth ||
        textureCanvas.height !== targetHeight;
      if (resized) {
        textureCanvas.width = targetWidth;
        textureCanvas.height = targetHeight;
      }
      textureContext.fillStyle = terminal.options.theme.background || '#07090e';
      textureContext.fillRect(0, 0, textureCanvas.width, textureCanvas.height);
      textureContext.imageSmoothingEnabled = false;
      drawWarmTerminalImage(viewport, targetWidth, targetHeight);
      applyScanlines();
      updateImageScale();
      screenResolution.x = targetWidth;
      screenResolution.y = targetHeight;
      uploadScreenTexture(resized);
    }

    function render(timestamp) {
      animationFrame = 0;
      if (!active || usingFallback || !gl) return;
      if (screenNeedsUpdate) {
        updateScreenTexture();
        screenNeedsUpdate = false;
      }
      const now = Number.isFinite(timestamp) ? timestamp : Date.now();
      if (reflectionAnimationStart === null) reflectionAnimationStart = now;
      const elapsed = now - reflectionAnimationStart;
      const lightPos = calculateLightPosition(elapsed, screenWidth);

      // Pass 1: Render curved CRT surface, thick-glass refraction, Newtonian spectral dispersion,
      // sub-surface matte light illumination, internal glyph glint, and outer Fresnel into sceneFbo.
      gl.bindFramebuffer(gl.FRAMEBUFFER, sceneFbo);
      gl.viewport(0, 0, fboWidth, fboHeight);
      gl.enable(gl.DEPTH_TEST);
      gl.depthFunc(gl.LEQUAL);
      gl.depthMask(true);
      gl.clearColor(0.0021, 0.0030, 0.0051, 1.0);
      gl.clearDepth(1.0);
      gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);

      gl.useProgram(crtProgram);
      gl.uniformMatrix4fv(crtUniforms.uViewProjMatrix, false, viewProjMatrix);
      gl.uniform2f(crtUniforms.uScreenSize, screenWidth, screenHeight);
      gl.uniform2f(crtUniforms.uResolution, screenResolution.x, screenResolution.y);
      gl.uniform2f(crtUniforms.uImageScale, imageScale.x, imageScale.y);
      gl.uniform3f(
        crtUniforms.uTerminalBackground,
        terminalBackgroundRgb[0],
        terminalBackgroundRgb[1],
        terminalBackgroundRgb[2]
      );
      gl.uniform3f(
        crtUniforms.uScreenBackground,
        screenBackgroundRgb[0],
        screenBackgroundRgb[1],
        screenBackgroundRgb[2]
      );
      gl.uniform3f(crtUniforms.uCameraPos, cameraEye.x, cameraEye.y, cameraEye.z);
      gl.uniform3f(crtUniforms.uLightPos, lightPos.x, lightPos.y, lightPos.z);
      gl.uniform1f(crtUniforms.uLightRadius, lightPos.radius);
      gl.uniform3f(
        crtUniforms.uEllipsoidPos,
        lightPos.ellipsoid.x,
        lightPos.ellipsoid.y,
        lightPos.ellipsoid.z
      );
      gl.uniform3f(
        crtUniforms.uEllipsoidRadii,
        lightPos.ellipsoid.rx,
        lightPos.ellipsoid.ry,
        lightPos.ellipsoid.rz
      );
      gl.uniform1f(crtUniforms.uOpticalEffects, opticalEffects);
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, screenTex);
      gl.uniform1i(crtUniforms.uScreen, 0);

      gl.bindVertexArray(crtVao);
      gl.drawElements(gl.TRIANGLES, crtIndexCount, gl.UNSIGNED_SHORT, 0);

      // Pass 2: Horizontal separable 13-tap Gaussian depth-of-field blur into blurFbo.
      gl.disable(gl.DEPTH_TEST);
      gl.depthMask(false);
      gl.bindFramebuffer(gl.FRAMEBUFFER, blurFbo);
      gl.viewport(0, 0, fboWidth, fboHeight);
      gl.useProgram(blurHProgram);
      bindBlurUniforms(blurHUniforms, sceneColorTex, 1.0, 0.0);
      gl.bindVertexArray(quadVao);
      gl.drawArrays(gl.TRIANGLES, 0, 6);

      // Pass 3: Vertical separable 13-tap Gaussian depth-of-field blur + ACES tone map to canvas.
      gl.bindFramebuffer(gl.FRAMEBUFFER, null);
      gl.viewport(0, 0, fboWidth, fboHeight);
      gl.useProgram(blurVProgram);
      bindBlurUniforms(blurVUniforms, blurColorTex, 0.0, 1.0);
      gl.bindVertexArray(quadVao);
      gl.drawArrays(gl.TRIANGLES, 0, 6);
      gl.bindVertexArray(null);

      requestFrame();
    }

    function bindBlurUniforms(uniforms, colorTexture, dirX, dirY) {
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, colorTexture);
      gl.uniform1i(uniforms.sceneColor, 0);
      gl.activeTexture(gl.TEXTURE1);
      gl.bindTexture(gl.TEXTURE_2D, sceneDepthTex);
      gl.uniform1i(uniforms.sceneDepth, 1);
      gl.uniform2f(uniforms.resolution, postResolution.x, postResolution.y);
      gl.uniform2f(uniforms.direction, dirX, dirY);
      gl.uniformMatrix4fv(uniforms.projectionMatrixInverse, false, projMatrixInverse);
      gl.uniform3f(uniforms.focalPointView, focalPointView.x, focalPointView.y, focalPointView.z);
      gl.uniform1f(uniforms.nearestDistance, nearestDistance);
      gl.uniform1f(uniforms.farthestDistance, farthestDistance);
      gl.uniform1f(uniforms.maxBlurPixels, maxBlurPixels);
      gl.uniform1f(uniforms.opticalEffects, opticalEffects);
    }

    function resize() {
      if (!active || usingFallback || !gl) return;
      const bounds = stage.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0) return;
      keyboard.setNarrow(bounds.width <= 720);
      fitTerminal();
      const pixelRatio = Math.min(global.devicePixelRatio || 1, 2);
      const drawWidth = Math.max(1, Math.floor(bounds.width * pixelRatio));
      const drawHeight = Math.max(1, Math.floor(bounds.height * pixelRatio));
      if (canvas.width !== drawWidth || canvas.height !== drawHeight) {
        canvas.width = drawWidth;
        canvas.height = drawHeight;
      }
      cameraAspect = bounds.width / bounds.height;
      mat4Perspective(projMatrix, cameraFov, cameraAspect, cameraNear, cameraFar);
      mat4InvertPerspective(projMatrixInverse, projMatrix);
      updateScreenDimensions(cameraAspect);
      frameCamera();
      resizePostProcess(drawWidth, drawHeight);
      requestFrame(true);
    }

    function resizePostProcess(width, height) {
      if (!gl || !sceneFbo || !blurFbo) return;
      if (fboWidth !== width || fboHeight !== height) {
        fboWidth = width;
        fboHeight = height;

        gl.activeTexture(gl.TEXTURE0);
        gl.bindTexture(gl.TEXTURE_2D, sceneColorTex);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.SRGB8_ALPHA8, width, height, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);

        gl.bindTexture(gl.TEXTURE_2D, sceneDepthTex);
        gl.texImage2D(
          gl.TEXTURE_2D,
          0,
          gl.DEPTH_COMPONENT24,
          width,
          height,
          0,
          gl.DEPTH_COMPONENT,
          gl.UNSIGNED_INT,
          null
        );

        gl.bindTexture(gl.TEXTURE_2D, blurColorTex);
        gl.texImage2D(gl.TEXTURE_2D, 0, gl.SRGB8_ALPHA8, width, height, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);
      }
      postResolution.x = width;
      postResolution.y = height;
      updateBlurDistanceRange();
    }

    function updateBlurDistanceRange() {
      const bounds = calculateFocalBounds(
        cameraEye,
        cameraUp,
        viewMatrix,
        screenWidth,
        screenHeight,
        flat
      );
      if (!Number.isFinite(bounds.nearestDistance) || !Number.isFinite(bounds.farthestDistance)) return;
      focalPointView.x = bounds.focalPointView.x;
      focalPointView.y = bounds.focalPointView.y;
      focalPointView.z = bounds.focalPointView.z;
      nearestDistance = bounds.nearestDistance;
      farthestDistance = bounds.farthestDistance;
    }

    function updateScreenDimensions(aspect) {
      if (!Number.isFinite(aspect) || aspect <= 0) return;
      const clampedAspect = clampScreenAspect(aspect);
      const area = baseScreenWidth * baseScreenHeight;
      screenWidth = Math.sqrt(area * clampedAspect);
      screenHeight = Math.sqrt(area / clampedAspect);
      updateImageScale();
    }

    function updateImageScale() {
      if (!textureCanvas || textureCanvas.height <= 0 || screenHeight <= 0) return;
      const fit = calculateImageScale(
        textureCanvas.width,
        textureCanvas.height,
        screenWidth,
        screenHeight
      );
      const minFit = Math.min(fit.x, fit.y);
      const proportional = minFit >= 0.62 ? 1 : minFit / 0.62;
      const innerMargin = 0.011 * Math.min(screenWidth, screenHeight);
      const bezelScaleX = Math.max(0.5, 1 - (2 * innerMargin) / screenWidth);
      const bezelScaleY = Math.max(0.5, 1 - (2 * innerMargin) / screenHeight);
      imageScale.x = bezelScaleX * (fit.x < fit.y ? proportional : 1);
      imageScale.y = bezelScaleY * (fit.y < fit.x ? proportional : 1);
    }

    function applyScanlines() {
      textureContext.save();
      textureContext.globalCompositeOperation = 'multiply';
      textureContext.fillStyle = 'rgba(0, 0, 0, 0.48)';
      for (let y = scanlinePeriod - 1; y < textureCanvas.height; y += scanlinePeriod) {
        textureContext.fillRect(0, y, textureCanvas.width, 1);
      }
      textureContext.restore();
    }

    function projectScreenBounds(target, distance, halfTan, aspect) {
      const scale = distance / cameraOffsetLength;
      const camX = target.x + cameraOffset.x * scale;
      const camY = target.y + cameraOffset.y * scale;
      const camZ = target.z + cameraOffset.z * scale;

      let fx = target.x - camX;
      let fy = target.y - camY;
      let fz = target.z - camZ;
      const fLen = Math.hypot(fx, fy, fz) || 1;
      fx /= fLen;
      fy /= fLen;
      fz /= fLen;

      // right = normalize(cross(f, (0, 1, 0))) = normalize((-fz, 0, fx))
      let rx = -fz;
      let ry = 0;
      let rz = fx;
      const rLen = Math.hypot(rx, ry, rz) || 1;
      rx /= rLen;
      ry /= rLen;
      rz /= rLen;

      // up = cross(r, f)
      const ux = ry * fz - rz * fy;
      const uy = rz * fx - rx * fz;
      const uz = rx * fy - ry * fx;

      let minX = Infinity;
      let maxX = -Infinity;
      let minY = Infinity;
      let maxY = -Infinity;
      let leftSum = 0;
      let rightSum = 0;
      let bottomSum = 0;
      let topSum = 0;
      const samples = [-1, -0.5, 0, 0.5, 1];
      const projectPoint = (nx, ny) => {
        const px = nx * (screenWidth / 2);
        const py = ny * (screenHeight / 2);
        const pz = flat ? 0 : -CRT_CURVE_KZ * (nx * nx + CRT_CURVE_AY * ny * ny);
        const vx = px - camX;
        const vy = py - camY;
        const vz = pz - camZ;
        const camSpaceX = vx * rx + vy * ry + vz * rz;
        const camSpaceY = vx * ux + vy * uy + vz * uz;
        const camSpaceZ = vx * fx + vy * fy + vz * fz;
        const depth = Math.max(0.001, camSpaceZ);
        return {
          x: camSpaceX / (depth * halfTan * aspect),
          y: camSpaceY / (depth * halfTan)
        };
      };
      for (const t of samples) {
        const leftPt = projectPoint(-1, t);
        const rightPt = projectPoint(1, t);
        const bottomPt = projectPoint(t, -1);
        const topPt = projectPoint(t, 1);
        leftSum += leftPt.x;
        rightSum += rightPt.x;
        bottomSum += bottomPt.y;
        topSum += topPt.y;
        if (leftPt.x < minX) minX = leftPt.x;
        if (rightPt.x > maxX) maxX = rightPt.x;
        if (bottomPt.y < minY) minY = bottomPt.y;
        if (topPt.y > maxY) maxY = topPt.y;
      }
      const count = samples.length;
      return {
        minX: 0.5 * (minX + leftSum / count),
        maxX: 0.5 * (maxX + rightSum / count),
        minY: 0.5 * (minY + bottomSum / count),
        maxY: 0.5 * (maxY + topSum / count),
        rx,
        ry,
        rz,
        ux,
        uy,
        uz
      };
    }

    function frameCamera() {
      const bounds = stage.getBoundingClientRect();
      const stageWidth = Math.max(1, bounds.width || root.clientWidth || 1);
      const stageHeight = Math.max(1, bounds.height || root.clientHeight || 1);
      const aspect = cameraAspect || (stageWidth / stageHeight) || 1.6;
      const halfFov = (cameraFov * Math.PI) / 360;
      const halfTan = Math.tan(halfFov);
      const area = baseScreenWidth * baseScreenHeight;

      const marginPx = 0.058 * Math.min(stageWidth, stageHeight);
      const targetSpanX = Math.max(0.2, 2 * (1 - (2 * marginPx) / stageWidth));
      const targetSpanY = Math.max(0.2, 2 * (1 - (2 * marginPx) / stageHeight));

      let distance = Math.max(
        screenHeight / (targetSpanY * halfTan),
        screenWidth / (targetSpanX * halfTan * aspect)
      );
      const target = { x: cameraTarget.x, y: cameraTarget.y, z: cameraTarget.z };

      for (let step = 0; step < 5; step += 1) {
        const projected = projectScreenBounds(target, distance, halfTan, aspect);
        const spanX = projected.maxX - projected.minX;
        const spanY = projected.maxY - projected.minY;
        if (!(spanX > 0) || !(spanY > 0)) break;

        const centerX = (projected.minX + projected.maxX) / 2;
        const centerY = (projected.minY + projected.maxY) / 2;
        const shiftRight = centerX * distance * halfTan * aspect;
        const shiftUp = centerY * distance * halfTan;
        target.x += projected.rx * shiftRight + projected.ux * shiftUp;
        target.y += projected.ry * shiftRight + projected.uy * shiftUp;
        target.z += projected.rz * shiftRight + projected.uz * shiftUp;

        screenWidth *= targetSpanX / spanX;
        screenHeight *= targetSpanY / spanY;
        const stepAspect = clampScreenAspect(screenWidth / screenHeight);
        screenWidth = Math.sqrt(area * stepAspect);
        screenHeight = Math.sqrt(area / stepAspect);
        distance = Math.max(
          distance * (projected.maxY - projected.minY) / targetSpanY,
          distance * (projected.maxX - projected.minX) / targetSpanX
        );
      }

      updateImageScale();

      const scale = distance / cameraOffsetLength;
      cameraEye.x = target.x + cameraOffset.x * scale;
      cameraEye.y = target.y + cameraOffset.y * scale;
      cameraEye.z = target.z + cameraOffset.z * scale;
      cameraLookTarget.x = target.x;
      cameraLookTarget.y = target.y;
      cameraLookTarget.z = target.z;
      mat4LookAt(viewMatrix, cameraEye, cameraLookTarget, worldUp, cameraUp);
      mat4Multiply(viewProjMatrix, projMatrix, viewMatrix);
    }

    function compileShader(type, source) {
      const shader = gl.createShader(type);
      gl.shaderSource(shader, source);
      gl.compileShader(shader);
      if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) {
        const log = gl.getShaderInfoLog(shader) || 'Unknown WebGL2 shader compilation error';
        gl.deleteShader(shader);
        throw new Error(log);
      }
      return shader;
    }

    function createProgram(vertexSource, fragmentSource, uniformNames) {
      const vs = compileShader(gl.VERTEX_SHADER, vertexSource);
      const fs = compileShader(gl.FRAGMENT_SHADER, fragmentSource);
      const program = gl.createProgram();
      gl.attachShader(program, vs);
      gl.attachShader(program, fs);
      gl.linkProgram(program);
      gl.deleteShader(vs);
      gl.deleteShader(fs);
      if (!gl.getProgramParameter(program, gl.LINK_STATUS)) {
        const log = gl.getProgramInfoLog(program) || 'Unknown WebGL2 program link error';
        gl.deleteProgram(program);
        throw new Error(log);
      }
      const locations = {};
      for (const name of uniformNames) {
        locations[name] = gl.getUniformLocation(program, name);
      }
      return { program, locations };
    }

    function createCrtMesh() {
      const segX = 96;
      const segY = 56;
      const vertexCount = (segX + 1) * (segY + 1);
      const vertices = new Float32Array(vertexCount * 2);
      let vOffset = 0;
      for (let iy = 0; iy <= segY; iy += 1) {
        const v = iy / segY;
        for (let ix = 0; ix <= segX; ix += 1) {
          const u = ix / segX;
          vertices[vOffset++] = u;
          vertices[vOffset++] = v;
        }
      }
      const indices = new Uint16Array(segX * segY * 6);
      let iOffset = 0;
      const rowStride = segX + 1;
      for (let iy = 0; iy < segY; iy += 1) {
        for (let ix = 0; ix < segX; ix += 1) {
          const a = iy * rowStride + ix;
          const b = a + 1;
          const c = a + rowStride;
          const d = c + 1;
          indices[iOffset++] = a;
          indices[iOffset++] = b;
          indices[iOffset++] = c;
          indices[iOffset++] = b;
          indices[iOffset++] = d;
          indices[iOffset++] = c;
        }
      }
      crtIndexCount = indices.length;
      crtVao = gl.createVertexArray();
      crtVbo = gl.createBuffer();
      crtIbo = gl.createBuffer();
      gl.bindVertexArray(crtVao);
      gl.bindBuffer(gl.ARRAY_BUFFER, crtVbo);
      gl.bufferData(gl.ARRAY_BUFFER, vertices, gl.STATIC_DRAW);
      gl.enableVertexAttribArray(0);
      gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 8, 0);
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, crtIbo);
      gl.bufferData(gl.ELEMENT_ARRAY_BUFFER, indices, gl.STATIC_DRAW);
      gl.bindVertexArray(null);
    }

    function createFullscreenQuad() {
      const quadVerts = new Float32Array([
        0, 0,
        1, 0,
        0, 1,
        0, 1,
        1, 0,
        1, 1
      ]);
      quadVao = gl.createVertexArray();
      quadVbo = gl.createBuffer();
      gl.bindVertexArray(quadVao);
      gl.bindBuffer(gl.ARRAY_BUFFER, quadVbo);
      gl.bufferData(gl.ARRAY_BUFFER, quadVerts, gl.STATIC_DRAW);
      gl.enableVertexAttribArray(0);
      gl.vertexAttribPointer(0, 2, gl.FLOAT, false, 8, 0);
      gl.bindVertexArray(null);
    }

    function createCrtShaderProgram() {
      const vertexShader = `#version 300 es
        precision highp float;
        layout(location = 0) in vec2 aUv;
        uniform mat4 uViewProjMatrix;
        uniform vec2 uScreenSize;
        uniform float uOpticalEffects;
        out vec2 vUv;
        out vec2 vSignedUv;
        out vec3 vWorldPos;
        out vec3 vOuterNormal;
        void main() {
          vUv = aUv;
          vec2 signedUv = aUv * 2.0 - 1.0;
          vSignedUv = signedUv;
          float r2 = signedUv.x * signedUv.x + 0.72 * signedUv.y * signedUv.y;
          float z = -0.24 * r2 * uOpticalEffects;
          vec3 worldPos = vec3(signedUv * (uScreenSize * 0.5), z);
          vWorldPos = worldPos;
          vec3 normal = normalize(vec3(
            uOpticalEffects * (0.96 * signedUv.x) / max(uScreenSize.x, 0.001),
            uOpticalEffects * (0.96 * 0.72 * signedUv.y) / max(uScreenSize.y, 0.001),
            1.0
          ));
          vOuterNormal = normal;
          gl_Position = uViewProjMatrix * vec4(worldPos, 1.0);
        }
      `;
      const fragmentShader = `#version 300 es
        precision highp float;
        uniform sampler2D uScreen;
        uniform vec2 uScreenSize;
        uniform vec2 uResolution;
        uniform vec2 uImageScale;
        uniform vec3 uTerminalBackground;
        uniform vec3 uScreenBackground;
        uniform vec3 uCameraPos;
        uniform vec3 uLightPos;
        uniform float uLightRadius;
        uniform vec3 uEllipsoidPos;
        uniform vec3 uEllipsoidRadii;
        uniform float uOpticalEffects;
        in vec2 vUv;
        in vec2 vSignedUv;
        in vec3 vWorldPos;
        in vec3 vOuterNormal;
        out vec4 fragColor;
        const float ELLIPSOID_BRIGHTNESS = 0.80;

        vec3 samplePhosphorSurface(vec2 surfaceUv) {
          vec2 imageUv = (surfaceUv - 0.5) / uImageScale + 0.5;
          bool outsideImage = any(greaterThan(abs(surfaceUv - 0.5), uImageScale * 0.5));
          vec3 color = uScreenBackground;
          if (!outsideImage) {
            color = texture(uScreen, imageUv).rgb;
          }
          float backgroundPixel = 1.0 - smoothstep(0.003, 0.018, distance(color, uTerminalBackground));
          return mix(color, uScreenBackground, backgroundPixel * 0.90);
        }

        float evaluateEllipsoidMatteLight(
          vec3 innerPos,
          vec3 innerNormal,
          vec3 internalViewDir,
          float iorChannel,
          vec3 outerNormal,
          float thickness
        ) {
          // 1. Large, solid-bodied vertically-elongated ellipsoid light surface (window pane / office light panel)
          vec3 toEllipsoid = uEllipsoidPos - innerPos;
          float rawDist = max(length(toEllipsoid), 0.1);
          vec3 L = toEllipsoid / rawDist;

          // Transmission through the outer curved glass into the slab
          float ndotlOut = max(dot(outerNormal, L), 0.0);
          float fIn = 0.0426 + (1.0 - 0.0426) * pow(1.0 - ndotlOut, 5.0);
          float transmitIn = (1.0 - fIn) * smoothstep(0.0, 0.28, ndotlOut);

          // Refract internal view ray & light ray across the curved glass
          vec3 lightEntryDir = refract(-L, outerNormal, 1.0 / iorChannel);
          vec3 lightInGlass = -lightEntryDir;
          float cosThetaL = max(dot(lightInGlass, outerNormal), 0.25);
          vec3 entryPos = innerPos + lightInGlass * (thickness / cosThetaL);

          // Trace where the matte-reflected internal cone meets the 3D ellipsoid plane at z = uEllipsoidPos.z:
          // This gives the light source a cohesive, solid window/fixture body with a soft blurred edge
          // instead of a point-centered galaxy nebula.
          vec3 matteBounceDir = normalize(reflect(-internalViewDir, innerNormal) + 0.45 * innerNormal);
          float tPlane = max((uEllipsoidPos.z - entryPos.z) / max(matteBounceDir.z, 0.20), 0.0);
          vec3 hitOnLightPlane = entryPos + matteBounceDir * tPlane;
          vec3 deltaLight = hitOnLightPlane - uEllipsoidPos;

          // Original smudge coordinates from the "Much better!" baseline:
          vec2 baseXY = vec2(
            deltaLight.x + 0.30 * deltaLight.y,
            deltaLight.y - 0.12 * deltaLight.x
          );
          // Pivot the 20-degree clockwise rotation around the visual center of the smudge on screen (baseXY ≈ (0.0, -0.75))
          // so it turns clockwise in place without swinging off to the left panel:
          vec2 centeredXY = vec2(baseXY.x - 0.15, baseXY.y + 0.75);
          vec2 turnedXY = vec2(
            0.939693 * centeredXY.x - 0.342020 * centeredXY.y,
            0.342020 * centeredXY.x + 0.939693 * centeredXY.y - 0.75
          );
          vec2 normXY = turnedXY / max(uEllipsoidRadii.xy * vec2(0.85, 1.35), vec2(0.05));
          // Extend downward by 20% more (1.2x taller overall) while keeping the top edge fixed
          normXY.y = (normXY.y + 0.20) / 1.20;

          // Tall window/light-bank metric: solid inner core with a much wider, softer falloff blur around it
          float bodyX = 1.0 - smoothstep(0.28, 1.95, abs(normXY.x));
          float bodyY = 1.0 - smoothstep(0.45, 1.85, abs(normXY.y));
          float windowBar = exp(-0.5 * pow((normXY.y - 0.55) / 0.12, 2.0)) * bodyX;
          float solidPanelBody = bodyX * bodyY;
          float wideFalloffBlur = (1.0 - smoothstep(0.10, 2.75, length(normXY * vec2(0.70, 0.52)))) * 0.52;

          // Refracted ray-bundle convergence (caustic density change from curved outer glass)
          float entryCurvature = length(entryPos.xy / (uScreenSize * 0.5));
          float refractionCompression = 1.0 + 0.42 * (1.0 - cosThetaL / max(ndotlOut, 0.22)) * (1.0 + 0.45 * entryCurvature);

          // Distance attenuation compensated for the 1.4x farther ellipsoid distance, scaled by 0.8 brightness
          float depthAtten = (15.8 * 0.80) / (rawDist * rawDist + 5.2);
          float ellipsoidSubsurface = transmitIn * depthAtten * refractionCompression *
            (solidPanelBody * 0.68 + wideFalloffBlur) * (1.0 - 0.4 * windowBar) * ELLIPSOID_BRIGHTNESS;

          // 2. Sub-surface contribution from the secondary sphere (crisper, less smudged, upper-left accent)
          vec3 toSphere = uLightPos - innerPos;
          float sphereDistSq = max(dot(toSphere, toSphere), 0.1);
          float tSphere = max(dot(toSphere, matteBounceDir), 0.0);
          vec3 closestSphereIn = toSphere - matteBounceDir * tSphere;
          float rSphereNorm = length(closestSphereIn) / max(uLightRadius * 0.92, 0.05);
          float sphereDisc = (1.0 - smoothstep(0.50, 1.25, rSphereNorm)) *
            (5.8 / (sphereDistSq + 2.2));

          return ellipsoidSubsurface + 0.28 * transmitIn * sphereDisc;
        }

        void main() {
          vec3 N = normalize(vOuterNormal);
          vec3 I = normalize(vWorldPos - uCameraPos);
          vec3 V = -I;
          float ndotv = max(dot(N, V), 0.001);

          // 1. Thick CRT glass tangent-frame Snell refraction & subtle Newtonian spectral dispersion
          float r2 = vSignedUv.x * vSignedUv.x + 0.72 * vSignedUv.y * vSignedUv.y;
          float thickness = uOpticalEffects * 0.036 * (1.0 + 0.24 * r2);
          vec3 refractG = refract(I, N, 1.0 / 1.520);
          float cosThetaT = max(dot(-refractG, N), 0.25);
          vec3 tangentRefract = refractG + cosThetaT * N;
          vec3 deltaPG = tangentRefract * (thickness / cosThetaT);
          vec2 deltaUvG = deltaPG.xy / uScreenSize;

          float grazing = 1.0 - ndotv * ndotv;
          float dispStrength = uOpticalEffects * (0.028 + 0.045 * grazing);
          vec2 uvR = vUv + deltaUvG * (1.0 - dispStrength);
          vec2 uvG = vUv + deltaUvG;
          vec2 uvB = vUv + deltaUvG * (1.0 + 1.18 * dispStrength);

          vec3 phosphorR = samplePhosphorSurface(uvR);
          vec3 phosphorG = samplePhosphorSurface(uvG);
          vec3 phosphorB = samplePhosphorSurface(uvB);
          vec3 terminalColor = vec3(phosphorR.r, phosphorG.g, phosphorB.b);

          // 3. Curvature density modulation (Jacobian lensing across curved glass meniscus)
          float lensDensity = 1.0 + uOpticalEffects * 0.28 * (1.0 - 0.65 * r2) *
            (cosThetaT / max(ndotv, 0.22) - 1.0);

          // 4. Dual-source sub-surface matte illumination & outer Fresnel reflections
          vec3 matteLight = vec3(0.0);
          float fOuter = 0.0426 + (1.0 - 0.0426) * pow(1.0 - ndotv, 5.0);
          vec3 outerReflection = vec3(0.0);

          if (uOpticalEffects > 0.5) {
            vec3 innerNormal = normalize(vec3(N.xy * (1.0 + 0.22 * r2), N.z));
            vec3 internalView = -refractG;
            vec3 slabVec = refractG * (thickness / cosThetaT);
            vec3 posInR = vWorldPos + slabVec * (1.0 - dispStrength * 2.2);
            vec3 posInG = vWorldPos + slabVec;
            vec3 posInB = vWorldPos + slabVec * (1.0 + dispStrength * 2.6);

            float mR = evaluateEllipsoidMatteLight(posInR, innerNormal, internalView, 1.492, N, thickness);
            float mG = evaluateEllipsoidMatteLight(posInG, innerNormal, internalView, 1.520, N, thickness);
            float mB = evaluateEllipsoidMatteLight(posInB, innerNormal, internalView, 1.555, N, thickness);

            // Pronounced sub-surface matte phosphor smudge with subtle Newtonian dispersion at its rim
            vec3 rawMatte = vec3(mR * 0.80, mG * 0.90, mB * 1.04) * vec3(0.99, 1.0, 1.015) * 0.56;
            float fadeY = smoothstep(0.02, 0.16, uvG.y) * (1.0 - smoothstep(0.93, 0.996, uvG.y));
            matteLight = rawMatte * lensDensity * (0.45 + 0.55 * fadeY);

            // 5. Outer-surface dielectric Fresnel reflection:
            //    - Crisper spherical light source with stronger Fresnel response
            //    - Cohesive outer-surface window/fixture reflection from the vertical ellipsoid
            vec3 reflectOut = reflect(I, N);
            vec3 toSphereOut = uLightPos - vWorldPos;
            float sphereDistSqOut = max(dot(toSphereOut, toSphereOut), 0.1);
            float projSphere = max(dot(toSphereOut, reflectOut), 0.0);
            vec3 closestSphere = toSphereOut - reflectOut * projSphere;
            float rSphereOut = length(closestSphere) / max(uLightRadius * 0.82, 0.05);
            // Solid spherical bulb profile with crisp edge (less blur than the ellipsoid)
            float sphereGlint = (1.0 - smoothstep(0.55, 1.22, rSphereOut)) *
              step(0.0, dot(toSphereOut, reflectOut)) *
              (7.4 / (sphereDistSqOut + 2.0));

            // Boosted Fresnel curve for the spherical accent light so it glints clearly off the glass
            float fSphere = 0.12 + (1.0 - 0.12) * pow(1.0 - ndotv, 2.1);

            vec3 toEllipOut = uEllipsoidPos - vWorldPos;
            float projEllip = max(dot(toEllipOut, reflectOut), 0.0);
            vec3 closestEllip = toEllipOut - reflectOut * projEllip;
            vec2 baseOutXY = vec2(
              closestEllip.x + 0.30 * closestEllip.y,
              closestEllip.y - 0.12 * closestEllip.x
            );
            vec2 centeredOutXY = vec2(baseOutXY.x - 0.15, baseOutXY.y + 0.75);
            vec2 rotEllipOut = vec2(
              0.939693 * centeredOutXY.x - 0.342020 * centeredOutXY.y,
              0.342020 * centeredOutXY.x + 0.939693 * centeredOutXY.y - 0.75
            ) / max(uEllipsoidRadii.xy * vec2(0.80, 1.28), vec2(0.05));
            rotEllipOut.y = (rotEllipOut.y + 0.20) / 1.20;
            float ellipOutBody = (1.0 - smoothstep(0.28, 1.90, abs(rotEllipOut.x))) *
              (1.0 - smoothstep(0.45, 1.80, abs(rotEllipOut.y)));
            float ellipOuterSheen = ellipOutBody *
              step(0.0, dot(toEllipOut, reflectOut)) *
              ((9.2 * 0.80) / (dot(toEllipOut, toEllipOut) + 5.0));

            vec3 ambientGlass = vec3(0.74, 0.86, 1.0) * 0.075;
            vec3 sphereOuterColor = vec3(0.88, 0.94, 1.0) * sphereGlint * 1.65;
            vec3 ellipOuterColor = vec3(0.80, 0.90, 1.0) * ellipOuterSheen * (0.68 * 0.80) * ELLIPSOID_BRIGHTNESS;
            outerReflection = fOuter * (ambientGlass + ellipOuterColor) + fSphere * sphereOuterColor;

            // 2. Outward curvature-directed internal glyph reflection with continuous blurred halo & glint:
            //    Zero-shift point sits where the curved screen is perpendicular to the low-right camera (~0.60, 0.42 in GL UV,
            //    i.e. 0.6/0.6 from top-left), where N_apex ≈ (+0.070, -0.068). Displacement is scaled to ~55% of the prior step.
            vec2 outwardDir = vec2(1.15 * (N.x - 0.070), 0.55 * (N.y + 0.068));
            vec2 reflectShift = (outwardDir * (thickness * 0.80)) / uScreenSize;
            vec2 bounceCenter = uvG - reflectShift;
            vec2 texel = 1.0 / max(uResolution, vec2(1.0));
            vec2 radialAxis = length(outwardDir) > 0.001 ? normalize(outwardDir) : vec2(-1.0, 0.0);
            vec2 orthoAxis = vec2(-radialAxis.y, radialAxis.x);
            float apexDist2 = dot(vUv - vec2(0.60, 0.42), vUv - vec2(0.60, 0.42)) * 2.4;
            vec2 blurScale = texel * (4.8 + 4.2 * apexDist2);

            vec3 bAvg = vec3(0.0);
            const vec2 kernel[16] = vec2[16](
              vec2(-1.20,  0.00),
              vec2(-0.75,  0.00),
              vec2(-0.35,  0.00),
              vec2( 0.00,  0.00),
              vec2( 0.35,  0.00),
              vec2( 0.75,  0.00),
              vec2( 1.20,  0.00),
              vec2(-0.85,  0.48),
              vec2(-0.28,  0.52),
              vec2( 0.28,  0.52),
              vec2( 0.85,  0.48),
              vec2(-0.85, -0.48),
              vec2(-0.28, -0.52),
              vec2( 0.28, -0.52),
              vec2( 0.85, -0.48),
              vec2( 0.00,  0.98)
            );
            const float weights[16] = float[16](
              0.045, 0.068, 0.088, 0.098, 0.088, 0.068, 0.045,
              0.048, 0.066, 0.066, 0.048,
              0.048, 0.066, 0.066, 0.048,
              0.044
            );
            for (int i = 0; i < 16; i++) {
              vec2 offsetUv = (radialAxis * kernel[i].x * 1.15 + orthoAxis * kernel[i].y * 0.90) * blurScale;
              vec3 tapColor = max(samplePhosphorSurface(bounceCenter + offsetUv) - uScreenBackground * 1.12, vec3(0.0));
              bAvg += tapColor * weights[i];
            }
            float viewGrazing = clamp(1.0 - ndotv, 0.0, 1.0);
            float curvatureFactor = smoothstep(0.04, 0.95, apexDist2);
            // Add a restrained spectral split to the blurred glyph reflection; the final composite blur softens it further.
            vec2 spectralShift = radialAxis * texel * (0.8 + 1.6 * curvatureFactor);
            vec3 spectralRed = max(samplePhosphorSurface(bounceCenter + spectralShift) - uScreenBackground * 1.12, vec3(0.0));
            vec3 spectralBlue = max(samplePhosphorSurface(bounceCenter - spectralShift) - uScreenBackground * 1.12, vec3(0.0));
            vec3 spectralGlyph = vec3(spectralRed.r, bAvg.g, spectralBlue.b);
            vec3 blurredGlyph = mix(bAvg, spectralGlyph, 0.20) * 2.15;
            float lightGlintBoost = 0.85 + 1.15 * clamp(mG + sphereGlint * 0.85, 0.0, 1.4);
            float glyphReflectWeight = (0.018 + 0.19 * curvatureFactor + 0.24 * pow(viewGrazing, 1.5)) * lightGlintBoost * 0.50;
            terminalColor += blurredGlyph * vec3(0.76, 0.90, 1.0) * glyphReflectWeight;
          }

          float grille = mix(1.0, 0.992 + 0.008 * cos(uvG.x * uResolution.x * 2.094), uOpticalEffects);
          float vignette = mix(
            1.0,
            1.0 - 0.10 * smoothstep(0.48, 0.90, length((uvG - 0.5) * vec2(1.0, 0.78))),
            uOpticalEffects
          );
          float edgeDistance = min(min(uvG.x, 1.0 - uvG.x), min(uvG.y, 1.0 - uvG.y));
          float edgeLine = 1.0 - smoothstep(0.002, 0.008, edgeDistance);

          vec3 innerComposite = (terminalColor * grille * vignette + matteLight) * (1.0 - fOuter * uOpticalEffects * 0.65);
          innerComposite = mix(innerComposite, vec3(0.045, 0.065, 0.085), edgeLine * 0.65);
          vec3 finalLinear = innerComposite + outerReflection;
          fragColor = vec4(finalLinear, 1.0);
        }
      `;
      const built = createProgram(vertexShader, fragmentShader, [
        'uViewProjMatrix',
        'uScreen',
        'uScreenSize',
        'uResolution',
        'uImageScale',
        'uTerminalBackground',
        'uScreenBackground',
        'uCameraPos',
        'uLightPos',
        'uLightRadius',
        'uEllipsoidPos',
        'uEllipsoidRadii',
        'uOpticalEffects'
      ]);
      crtProgram = built.program;
      crtUniforms = built.locations;
    }

    function createPostProcess() {
      sceneColorTex = gl.createTexture();
      gl.bindTexture(gl.TEXTURE_2D, sceneColorTex);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.SRGB8_ALPHA8, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);

      sceneDepthTex = gl.createTexture();
      gl.bindTexture(gl.TEXTURE_2D, sceneDepthTex);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.DEPTH_COMPONENT24, 1, 1, 0, gl.DEPTH_COMPONENT, gl.UNSIGNED_INT, null);

      sceneFbo = gl.createFramebuffer();
      gl.bindFramebuffer(gl.FRAMEBUFFER, sceneFbo);
      gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, sceneColorTex, 0);
      gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.TEXTURE_2D, sceneDepthTex, 0);

      blurColorTex = gl.createTexture();
      gl.bindTexture(gl.TEXTURE_2D, blurColorTex);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.SRGB8_ALPHA8, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);

      blurFbo = gl.createFramebuffer();
      gl.bindFramebuffer(gl.FRAMEBUFFER, blurFbo);
      gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, blurColorTex, 0);
      gl.bindFramebuffer(gl.FRAMEBUFFER, null);

      const vertexShader = `#version 300 es
        precision highp float;
        layout(location = 0) in vec2 aUv;
        out vec2 vUv;
        void main() {
          vUv = aUv;
          gl_Position = vec4(aUv * 2.0 - 1.0, 0.0, 1.0);
        }
      `;
      const fragmentShader = `#version 300 es
        precision highp float;
        uniform sampler2D sceneColor;
        uniform sampler2D sceneDepth;
        uniform vec2 resolution;
        uniform vec2 direction;
        uniform mat4 projectionMatrixInverse;
        uniform vec3 focalPointView;
        uniform float nearestDistance;
        uniform float farthestDistance;
        uniform float maxBlurPixels;
        uniform float opticalEffects;
        in vec2 vUv;
        out vec4 fragColor;

        vec3 acesFilmicToneMapping(vec3 color) {
          color *= 1.08 / 0.6;
          vec3 a = color * (color + 0.0245786) - 0.000090537;
          vec3 b = color * (0.983729 * color + 0.4329510) + 0.238081;
          return clamp(a / b, 0.0, 1.0);
        }

        vec3 linearToSRGB(vec3 value) {
          return mix(
            pow(max(value, vec3(0.0)), vec3(0.41666)) * 1.055 - vec3(0.055),
            value * 12.92,
             vec3(lessThanEqual(value, vec3(0.0031308)))
          );
        }

        void addGaussianTap(
          float tap,
          float weight,
          vec2 texel,
          float radius,
          inout vec4 colorSum,
          inout float weightSum
        ) {
          vec2 sampleUv = clamp(
            vUv + direction * texel * radius * (tap / 6.0),
            vec2(0.0),
            vec2(1.0)
          );
          float valid = step(texture(sceneDepth, sampleUv).r, 0.999999);
          colorSum += texture(sceneColor, sampleUv) * (weight * valid);
          weightSum += weight * valid;
        }

        void main() {
          float depth = texture(sceneDepth, vUv).r;
          vec4 source = texture(sceneColor, vUv);
          if (opticalEffects < 0.5) {
            fragColor = source;
            OUTPUT_COLOR
            return;
          }

          vec4 clipPosition = vec4(vUv * 2.0 - 1.0, depth * 2.0 - 1.0, 1.0);
          vec4 viewPosition = projectionMatrixInverse * clipPosition;
          viewPosition /= viewPosition.w;
          float distanceToFocalPoint = length(viewPosition.xyz - focalPointView);
          float distanceRange = max(farthestDistance - nearestDistance, 0.0001);
          float proximity = clamp(
            (farthestDistance - distanceToFocalPoint) / distanceRange,
            0.0,
            1.0
          );
          float blurAmount = proximity * proximity * step(depth, 0.999999);
          float radius = blurAmount * maxBlurPixels;
          vec2 texel = 1.0 / max(resolution, vec2(1.0));
          vec4 colorSum = vec4(0.0);
          float weightSum = 0.0;
          addGaussianTap(-6.0, 0.011109, texel, radius, colorSum, weightSum);
          addGaussianTap(-5.0, 0.043937, texel, radius, colorSum, weightSum);
          addGaussianTap(-4.0, 0.135335, texel, radius, colorSum, weightSum);
          addGaussianTap(-3.0, 0.324652, texel, radius, colorSum, weightSum);
          addGaussianTap(-2.0, 0.606531, texel, radius, colorSum, weightSum);
          addGaussianTap(-1.0, 0.882497, texel, radius, colorSum, weightSum);
          addGaussianTap(0.0, 1.0, texel, radius, colorSum, weightSum);
          addGaussianTap(1.0, 0.882497, texel, radius, colorSum, weightSum);
          addGaussianTap(2.0, 0.606531, texel, radius, colorSum, weightSum);
          addGaussianTap(3.0, 0.324652, texel, radius, colorSum, weightSum);
          addGaussianTap(4.0, 0.135335, texel, radius, colorSum, weightSum);
          addGaussianTap(5.0, 0.043937, texel, radius, colorSum, weightSum);
          addGaussianTap(6.0, 0.011109, texel, radius, colorSum, weightSum);
          vec4 blurred = colorSum / max(weightSum, 0.0001);
          fragColor = mix(source, blurred, step(0.0001, weightSum));
          OUTPUT_COLOR
        }
      `;
      const outputTransform = 'fragColor.rgb = linearToSRGB(acesFilmicToneMapping(fragColor.rgb));';
      const horizontalShader = fragmentShader.replace(/OUTPUT_COLOR/g, '');
      const finalShader = fragmentShader.replace(/OUTPUT_COLOR/g, outputTransform);
      const blurUniformNames = [
        'sceneColor',
        'sceneDepth',
        'resolution',
        'direction',
        'projectionMatrixInverse',
        'focalPointView',
        'nearestDistance',
        'farthestDistance',
        'maxBlurPixels',
        'opticalEffects'
      ];
      const builtH = createProgram(vertexShader, horizontalShader, blurUniformNames);
      blurHProgram = builtH.program;
      blurHUniforms = builtH.locations;
      const builtV = createProgram(vertexShader, finalShader, blurUniformNames);
      blurVProgram = builtV.program;
      blurVUniforms = builtV.locations;
      createFullscreenQuad();
    }

    function screenCoordinates(event) {
      const bounds = canvas.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0) return null;
      const ndcX = Math.max(-1, Math.min(1, ((event.clientX - bounds.left) / bounds.width) * 2 - 1));
      const ndcY = Math.max(-1, Math.min(1, -((event.clientY - bounds.top) / bounds.height) * 2 + 1));

      // Unproject NDC ray into camera view space, then world space
      const viewX = ndcX * projMatrixInverse[0];
      const viewY = ndcY * projMatrixInverse[5];
      const viewZ = -1.0;
      const dirWorld = {
        x: viewMatrix[0] * viewX + viewMatrix[1] * viewY + viewMatrix[2] * viewZ,
        y: viewMatrix[4] * viewX + viewMatrix[5] * viewY + viewMatrix[6] * viewZ,
        z: viewMatrix[8] * viewX + viewMatrix[9] * viewY + viewMatrix[10] * viewZ
      };

      const hit = solveParaboloidRayHit(cameraEye, dirWorld, screenWidth, screenHeight, flat);
      if (!hit) return null;
      const refracted = refractThickGlassUV(
        { x: hit.u, y: hit.v },
        cameraEye,
        screenWidth,
        screenHeight,
        flat
      );
      const u = (refracted.uvG.x - 0.5) / (imageScale.x || 1) + 0.5;
      const v = (refracted.uvG.y - 0.5) / (imageScale.y || 1) + 0.5;
      const clampedU = Math.max(0, Math.min(1, u));
      const clampedV = Math.max(0, Math.min(1, v));
      const targetBounds = terminalScreen.getBoundingClientRect();
      return {
        clientX: targetBounds.left + clampedU * targetBounds.width,
        clientY: targetBounds.top + (1 - clampedV) * targetBounds.height
      };
    }

    function forwardMouseEvent(event) {
      const coordinates = screenCoordinates(event);
      if (!coordinates) return;
      const EventConstructor = event.type === 'wheel' ? global.WheelEvent : global.MouseEvent;
      const forwarded = new EventConstructor(event.type, {
        bubbles: true,
        cancelable: true,
        view: global,
        clientX: coordinates.clientX,
        clientY: coordinates.clientY,
        screenX: event.screenX,
        screenY: event.screenY,
        button: event.button,
        buttons: event.buttons,
        detail: event.detail,
        ctrlKey: event.ctrlKey,
        shiftKey: event.shiftKey,
        altKey: event.altKey,
        metaKey: event.metaKey,
        deltaX: event.deltaX,
        deltaY: event.deltaY,
        deltaMode: event.deltaMode
      });
      terminalScreen.dispatchEvent(forwarded);
      if (event.cancelable) event.preventDefault();
      if (event.type === 'mousedown') terminal.focus();
    }

    function onCanvasMove(event) {
      if (!dragging) forwardMouseEvent(event);
    }

    function onCanvasDown(event) {
      dragging = true;
      forwardMouseEvent(event);
    }

    function onWindowMove(event) {
      if (dragging) forwardMouseEvent(event);
    }

    function onWindowUp(event) {
      if (!dragging) return;
      forwardMouseEvent(event);
      dragging = false;
    }

    try {
      fitTerminal();
      addon = new WebglAddon(true);
      terminal.loadAddon(addon);
      screenCanvas = addon._renderer && addon._renderer._canvas;
      if (!screenCanvas || screenCanvas.width <= 0 || screenCanvas.height <= 0) {
        throw new Error('The xterm WebGL addon did not expose its rendered screen canvas');
      }

      gl = rendererContextProbe(canvas);
      if (!gl) throw new Error('WebGL2 is unavailable');
      if (typeof gl.getExtension === 'function' && typeof gl.getParameter === 'function') {
        const rendererInfo = gl.getExtension('WEBGL_debug_renderer_info');
        if (rendererInfo) {
          const rendererName = gl.getParameter(rendererInfo.UNMASKED_RENDERER_WEBGL);
          if (/swiftshader|software rasterizer|llvmpipe/i.test(rendererName)) {
            throw new Error(`software WebGL renderer detected (${rendererName})`);
          }
        }
      }

      createCrtMesh();
      createCrtShaderProgram();
      createPostProcess();

      textureCanvas = document.createElement('canvas');
      const viewport = getScreenViewport();
      textureCanvas.width = viewport.width * upscaleFactor;
      textureCanvas.height = viewport.height * upscaleFactor;
      textureContext = textureCanvas.getContext('2d', { alpha: false });
      if (!textureContext) {
        throw new Error('A 2D canvas is unavailable for transferring the xterm screen');
      }
      textureContext.fillStyle = terminal.options.theme.background || '#07090e';
      textureContext.fillRect(0, 0, textureCanvas.width, textureCanvas.height);
      textureContext.imageSmoothingEnabled = false;
      drawWarmTerminalImage(viewport, textureCanvas.width, textureCanvas.height);
      applyScanlines();
      screenResolution.x = textureCanvas.width;
      screenResolution.y = textureCanvas.height;
      uploadScreenTexture(true);

      if (!terminal._core || !terminal._core._renderService ||
          typeof terminal._core._renderService.onRender !== 'function') {
        throw new Error('This xterm.js version does not expose screen redraw notifications required by the CRT surface');
      }
      renderSubscription = terminal._core._renderService.onRender(() => requestFrame(true));
      contextLossSubscription = addon.onContextLoss(() => {
        fallback(new Error('xterm WebGL context was lost'));
      });

      canvas.addEventListener('mousemove', onCanvasMove);
      canvas.addEventListener('mousedown', onCanvasDown);
      canvas.addEventListener('click', forwardMouseEvent);
      canvas.addEventListener('dblclick', forwardMouseEvent);
      canvas.addEventListener('wheel', forwardMouseEvent, { passive: false });
      canvas.addEventListener('contextmenu', forwardMouseEvent);
      canvas.addEventListener('webglcontextlost', onWebGLContextLost);
      global.addEventListener('mousemove', onWindowMove);
      global.addEventListener('mouseup', onWindowUp);
      resizeObserver = new global.ResizeObserver(resize);
      resizeObserver.observe(root);
      updateScreenDimensions(stage.clientWidth / stage.clientHeight);
      resize();
      requestFrame(true);
    } catch (error) {
      fallback(error);
    }

    return {
      fitTerminal,
      resize,
      fallback,
      isFallback: () => usingFallback,
      handleKeyEvent(event) {
        return keyboard.handleKeyEvent(event);
      },
      dispose() {
        if (!active) return;
        active = false;
        removeInputListeners();
        disposeScene();
        if (addon) addon.dispose();
        addon = null;
        keyboard.dispose();
        stage.remove();
        root.style.opacity = '';
        root.style.pointerEvents = '';
      }
    };
  }

  function rendererContextProbe(canvas) {
    if (!canvas || typeof canvas.getContext !== 'function') return null;
    return canvas.getContext('webgl2', {
      antialias: true,
      alpha: false,
      depth: false,
      stencil: false
    });
  }

  const REFERENCE_GRID_COLS = 72;
  const REFERENCE_GRID_ROWS = 20;
  const TARGET_GRID_CELLS = REFERENCE_GRID_COLS * REFERENCE_GRID_ROWS;
  const REFERENCE_SCREEN_ASPECT = 16 / 10;
  const MIN_SCREEN_ASPECT = 8 / 16;
  const MAX_SCREEN_ASPECT = 16 / 8;
  const REFERENCE_GRID_ASPECT = REFERENCE_GRID_COLS / REFERENCE_GRID_ROWS;
  const MIN_GRID_ASPECT = REFERENCE_GRID_ASPECT * (MIN_SCREEN_ASPECT / REFERENCE_SCREEN_ASPECT);
  const MAX_GRID_ASPECT = REFERENCE_GRID_ASPECT * (MAX_SCREEN_ASPECT / REFERENCE_SCREEN_ASPECT);
  const MIN_GRID_COLS = Math.round(Math.sqrt(TARGET_GRID_CELLS * MIN_GRID_ASPECT));
  const MAX_GRID_COLS = Math.round(Math.sqrt(TARGET_GRID_CELLS * MAX_GRID_ASPECT));
  const MIN_GRID_ROWS = Math.round(TARGET_GRID_CELLS / MAX_GRID_COLS);
  const MAX_GRID_ROWS = Math.round(TARGET_GRID_CELLS / MIN_GRID_COLS);

  function clampScreenAspect(aspect) {
    const valid = Number.isFinite(aspect) && aspect > 0 ? aspect : REFERENCE_SCREEN_ASPECT;
    return Math.min(MAX_SCREEN_ASPECT, Math.max(MIN_SCREEN_ASPECT, valid));
  }

  function calculateGrid(width, height) {
    const viewportAspect = width > 0 && height > 0 ? width / height : REFERENCE_SCREEN_ASPECT;
    const aspect = clampScreenAspect(viewportAspect);
    const requestedGridAspect = REFERENCE_GRID_ASPECT * (aspect / REFERENCE_SCREEN_ASPECT);
    const gridAspect = Math.min(MAX_GRID_ASPECT, Math.max(MIN_GRID_ASPECT, requestedGridAspect));
    const cols = Math.min(MAX_GRID_COLS, Math.max(MIN_GRID_COLS, Math.round(Math.sqrt(TARGET_GRID_CELLS * gridAspect))));
    const rows = Math.min(MAX_GRID_ROWS, Math.max(MIN_GRID_ROWS, Math.round(TARGET_GRID_CELLS / cols)));
    return { cols, rows };
  }

  function calculateImageScale(sourceWidth, sourceHeight, surfaceWidth, surfaceHeight) {
    const sourceAspect = sourceWidth > 0 && sourceHeight > 0
      ? sourceWidth / sourceHeight
      : surfaceWidth / surfaceHeight;
    const surfaceAspect = surfaceWidth / surfaceHeight;
    return {
      x: Math.min(1, sourceAspect / surfaceAspect),
      y: Math.min(1, surfaceAspect / sourceAspect)
    };
  }

  function calculateCaptureViewport(canvasWidth, canvasHeight, deviceWidth, deviceHeight) {
    if (deviceWidth > 0 && deviceHeight > 0 &&
        deviceWidth <= canvasWidth && deviceHeight <= canvasHeight) {
      return {
        x: 0,
        // The WebGL viewport is bottom-origin; drawImage uses top-origin canvas coordinates.
        y: canvasHeight - deviceHeight,
        width: deviceWidth,
        height: deviceHeight
      };
    }
    return { x: 0, y: 0, width: canvasWidth, height: canvasHeight };
  }

  const REFLECTION_PERIOD_MS = 5 * 60 * 1000;

  function calculateReflectionPhase(elapsedMs) {
    return ((elapsedMs % REFLECTION_PERIOD_MS) / REFLECTION_PERIOD_MS) * Math.PI * 2;
  }

  function calculateReflectionOffset(elapsedMs) {
    return 1.2 * Math.sin(calculateReflectionPhase(elapsedMs));
  }

  function calculateReflectionDepth(elapsedMs) {
    return 1 + 0.2 * (1 - Math.sin(calculateReflectionPhase(elapsedMs)));
  }

  global.KabibiBrowserSuperDemo = Object.freeze({
    start,
    createVirtualKeyboard,
    calculateGrid,
    calculateImageScale,
    calculateCaptureViewport,
    calculateReflectionOffset,
    calculateReflectionDepth,
    calculateLightPosition,
    solveParaboloidRayHit,
    refractThickGlassUV,
    calculateFocalBounds,
    GLASS_IOR_R,
    GLASS_IOR_G,
    GLASS_IOR_B,
    MIN_SCREEN_ASPECT,
    MAX_SCREEN_ASPECT,
    MIN_GRID_COLS,
    MAX_GRID_COLS,
    MIN_GRID_ROWS,
    MAX_GRID_ROWS
  });
})(globalThis);
