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
        grid-template-columns: repeat(18, minmax(0, 1fr));
        gap: clamp(2px, 0.42cqw, 7px);
        width: 100%;
      }
      .kabibi-keyboard--narrow .kabibi-keyboard-grid {
        grid-template-columns: repeat(9, minmax(0, 1fr));
      }
      .kabibi-key {
        position: relative;
        box-sizing: border-box;
        min-width: 0;
        height: clamp(2.25rem, 6dvh, 3.5rem);
        padding: 0 2px;
        overflow: hidden;
        border: 1px solid #030405;
        border-radius: clamp(4px, 0.55cqw, 9px);
        color: #d9e0e8;
        background:
          linear-gradient(180deg, #454b53 0%, #252a30 25%, #111417 100%);
        box-shadow:
          0 3px 0 #030405,
          0 4px 6px rgba(0,0,0,.65),
          inset 0 1px 1px rgba(255,255,255,.3),
          inset 0 -2px 3px rgba(0,0,0,.82);
        font: 600 clamp(.62rem, 1.45cqw, 1rem)/1 system-ui, sans-serif;
        text-shadow: 0 -1px 1px #000, 0 1px 1px rgba(255,255,255,.15);
        cursor: pointer;
        -webkit-tap-highlight-color: transparent;
      }
      .kabibi-key::before {
        position: absolute;
        inset: 1px 2px auto;
        height: 31%;
        border-radius: inherit;
        background: linear-gradient(180deg, rgba(255,255,255,.2), transparent);
        content: '';
        pointer-events: none;
      }
      .kabibi-key::after {
        position: absolute;
        top: 4px;
        right: 5px;
        width: 3px;
        height: 3px;
        border-radius: 50%;
        background: #aa7733;
        box-shadow: 0 0 3px 1px rgba(201,143,57,.38);
        content: '';
        opacity: .72;
        pointer-events: none;
      }
      .kabibi-key:active {
        transform: translateY(2px);
        background: linear-gradient(180deg, #252a30 0%, #101316 100%);
        box-shadow:
          0 1px 0 #030405,
          0 2px 3px rgba(0,0,0,.65),
          inset 0 1px 2px rgba(0,0,0,.8);
      }
      .kabibi-key:active::after {
        background: #ffd27a;
        box-shadow: 0 0 6px 2px rgba(255,178,62,.8);
        opacity: 1;
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
          border-radius: clamp(4px, 0.55vw, 9px);
          font-size: clamp(.62rem, 1.45vw, 1rem);
        }
      }
    `;
    const controls = document.createElement('div');
    controls.className = 'kabibi-keyboard';
    controls.setAttribute('role', 'group');
    controls.setAttribute('aria-label', 'Kabibi screen keyboard');
    const grid = document.createElement('div');
    grid.className = 'kabibi-keyboard-grid';
    controls.appendChild(grid);

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
      dispose() {
        controls.remove();
        style.remove();
      }
    };
  }

  function start({ terminal, fitAddon, root, terminalContainer, dependencies = {} }) {
    const THREE = dependencies.THREE;
    const WebglAddon = dependencies.WebglAddon;
    const document = dependencies.document || global.document;
    const flat = dependencies.flat === true;
    const upscaleFactor = 2;
    const scanlinePeriod = 4;
    if (!THREE || !WebglAddon) {
      throw new Error('Super-demo mode requires Three.js and the xterm.js WebGL addon');
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

    let addon;
    let renderer;
    let animationFrame = 0;
    let active = true;
    let usingFallback = false;
    let renderSubscription = null;
    let contextLossSubscription = null;
    let resizeObserver = null;
    let geometry = null;
    let screenMaterial = null;
    let glassMaterial = null;
    let reflectionMaterial = null;
    let screenTexture = null;
    let screenCanvas = null;
    let textureCanvas = null;
    let textureContext = null;
    let scene = null;
    let camera = null;
    let screenMesh = null;
    let glassMesh = null;
    let reflectionMesh = null;
    const raycaster = new THREE.Raycaster();
    const pointer = new THREE.Vector2();
    const hitPoint = new THREE.Vector2();
    const baseScreenWidth = 2.72;
    const baseScreenHeight = 1.58;
    let screenWidth = baseScreenWidth;
    let screenHeight = baseScreenHeight;
    const cameraTarget = { x: 0.015, y: -0.015, z: 0 };
    const cameraOffset = { x: 0.295, y: -0.205, z: 2.66 };
    const cameraOffsetLength = Math.hypot(cameraOffset.x, cameraOffset.y, cameraOffset.z);
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
      if (renderer) renderer.dispose();
      renderer = null;
      if (screenTexture) screenTexture.dispose();
      screenTexture = null;
      screenCanvas = null;
      textureCanvas = null;
      textureContext = null;
      if (screenMaterial) screenMaterial.dispose();
      screenMaterial = null;
      if (glassMaterial) glassMaterial.dispose();
      glassMaterial = null;
      if (reflectionMaterial) reflectionMaterial.dispose();
      reflectionMaterial = null;
      if (geometry) geometry.dispose();
      geometry = null;
      scene = null;
      camera = null;
      screenMesh = null;
      glassMesh = null;
      reflectionMesh = null;
    }

    function removeInputListeners() {
      canvas.removeEventListener('mousemove', onCanvasMove);
      canvas.removeEventListener('mousedown', onCanvasDown);
      canvas.removeEventListener('click', forwardMouseEvent);
      canvas.removeEventListener('dblclick', forwardMouseEvent);
      canvas.removeEventListener('wheel', forwardMouseEvent);
      canvas.removeEventListener('contextmenu', forwardMouseEvent);
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

    function requestFrame() {
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
      applyScanlines();
      updateImageScale();
      const resolution = screenMaterial.uniforms.resolution.value;
      if (resolution.x !== targetWidth || resolution.y !== targetHeight) {
        resolution.set(targetWidth, targetHeight);
      }
      if (resized) {
        const previousTexture = screenTexture;
        screenTexture = new THREE.CanvasTexture(textureCanvas);
        screenTexture.colorSpace = THREE.SRGBColorSpace;
        screenTexture.minFilter = THREE.LinearFilter;
        screenTexture.magFilter = THREE.LinearFilter;
        screenMaterial.uniforms.screen.value = screenTexture;
        if (previousTexture) previousTexture.dispose();
      } else {
        screenTexture.needsUpdate = true;
      }
    }

    function render() {
      animationFrame = 0;
      if (!active || usingFallback) return;
      updateScreenTexture();
      renderer.render(scene, camera);
    }

    function resize() {
      if (!active || usingFallback || !renderer) return;
      const bounds = stage.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0) return;
      keyboard.setNarrow(bounds.width <= 720);
      fitTerminal();
      const pixelRatio = Math.min(global.devicePixelRatio || 1, 2);
      renderer.setPixelRatio(pixelRatio);
      renderer.setSize(bounds.width, bounds.height, false);
      camera.aspect = bounds.width / bounds.height;
      camera.updateProjectionMatrix();
      updateScreenDimensions(camera.aspect);
      frameCamera();
      requestFrame();
    }

    function updateScreenDimensions(aspect) {
      if (!Number.isFinite(aspect) || aspect <= 0) return;
      const area = baseScreenWidth * baseScreenHeight;
      screenWidth = Math.sqrt(area * aspect);
      screenHeight = Math.sqrt(area / aspect);
      const scaleX = screenWidth / baseScreenWidth;
      const scaleY = screenHeight / baseScreenHeight;
      if (screenMesh) screenMesh.scale.set(scaleX, scaleY, 1);
      if (glassMesh) glassMesh.scale.set(scaleX, scaleY, 1);
      if (reflectionMesh) reflectionMesh.scale.set(scaleX, scaleY, 1);
      updateImageScale();
    }

    function updateImageScale() {
      if (!screenMaterial || !textureCanvas || textureCanvas.height <= 0 ||
          screenHeight <= 0) return;
      const fit = calculateImageScale(
        textureCanvas.width,
        textureCanvas.height,
        screenWidth,
        screenHeight
      );
      const minFit = Math.min(fit.x, fit.y);
      const proportional = minFit >= 0.84 ? 1 : minFit / 0.84;
      const innerMargin = 0.032 * Math.min(screenWidth, screenHeight);
      const bezelScaleX = Math.max(0.5, 1 - (2 * innerMargin) / screenWidth);
      const bezelScaleY = Math.max(0.5, 1 - (2 * innerMargin) / screenHeight);
      const scaleX = bezelScaleX * (fit.x < fit.y ? proportional : 1);
      const scaleY = bezelScaleY * (fit.y < fit.x ? proportional : 1);
      screenMaterial.uniforms.imageScale.value.set(scaleX, scaleY);
    }

    function applyScanlines() {
      textureContext.save();
      textureContext.globalCompositeOperation = 'multiply';
      textureContext.fillStyle = 'rgba(0, 0, 0, 0.30)';
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
        const pz = flat ? 0 : -0.24 * (nx * nx + 0.72 * ny * ny);
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
      const aspect = camera.aspect || (stageWidth / stageHeight) || 1.6;
      const halfFov = camera.fov * Math.PI / 360;
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
        const normalizeScale = Math.sqrt(area / (screenWidth * screenHeight));
        screenWidth *= normalizeScale;
        screenHeight *= normalizeScale;
        distance *= normalizeScale;
      }

      const scaleX = screenWidth / baseScreenWidth;
      const scaleY = screenHeight / baseScreenHeight;
      if (screenMesh) screenMesh.scale.set(scaleX, scaleY, 1);
      if (glassMesh) glassMesh.scale.set(scaleX, scaleY, 1);
      if (reflectionMesh) reflectionMesh.scale.set(scaleX, scaleY, 1);
      updateImageScale();

      const scale = distance / cameraOffsetLength;
      camera.position.set(
        target.x + cameraOffset.x * scale,
        target.y + cameraOffset.y * scale,
        target.z + cameraOffset.z * scale
      );
      camera.lookAt(target.x, target.y, target.z);
    }

    function makeGeometry() {
      const result = new THREE.PlaneGeometry(screenWidth, screenHeight, 96, 56);
      if (flat) return result;
      const position = result.attributes.position;
      for (let index = 0; index < position.count; index += 1) {
        const x = position.getX(index);
        const y = position.getY(index);
        const nx = x / (screenWidth / 2);
        const ny = y / (screenHeight / 2);
        position.setZ(index, -0.24 * (nx * nx + 0.72 * ny * ny));
      }
      position.needsUpdate = true;
      result.computeVertexNormals();
      return result;
    }

    function createScreenMaterial() {
      return new THREE.ShaderMaterial({
        uniforms: {
          screen: { value: screenTexture },
          resolution: { value: new THREE.Vector2(1, 1) },
          imageScale: { value: new THREE.Vector2(1, 1) },
          terminalBackground: { value: new THREE.Color(terminal.options.theme.background || '#07090e') },
          screenBackground: { value: new THREE.Color(0x101923) },
          opticalEffects: { value: flat ? 0 : 1 }
        },
        vertexShader: `
          varying vec2 vUv;
          void main() {
            vUv = uv;
            gl_Position = projectionMatrix * modelViewMatrix * vec4(position, 1.0);
          }
        `,
        fragmentShader: `
          uniform sampler2D screen;
          uniform vec2 resolution;
          uniform vec2 imageScale;
          uniform vec3 terminalBackground;
          uniform vec3 screenBackground;
          uniform float opticalEffects;
          varying vec2 vUv;
          void main() {
            vec2 imageUv = (vUv - 0.5) / imageScale + 0.5;
            bool outsideImage = any(greaterThan(abs(vUv - 0.5), imageScale * 0.5));
            float cornerDistance = length(vec2(1.0, 0.0) - imageUv);
            float lowerRight = 1.0 - smoothstep(0.08, 0.48, cornerDistance);
            float blur = lowerRight * opticalEffects;
            vec2 texel = 1.0 / max(resolution, vec2(1.0));
            vec2 offset = texel * blur * 8.0;
            vec4 color = vec4(screenBackground, 1.0);
            if (!outsideImage) {
              color = texture2D(screen, imageUv) * 0.40;
              color += texture2D(screen, imageUv + vec2(offset.x, 0.0)) * 0.10;
              color += texture2D(screen, imageUv - vec2(offset.x, 0.0)) * 0.10;
              color += texture2D(screen, imageUv + vec2(0.0, offset.y)) * 0.10;
              color += texture2D(screen, imageUv - vec2(0.0, offset.y)) * 0.10;
              color += texture2D(screen, imageUv + offset) * 0.05;
              color += texture2D(screen, imageUv - offset) * 0.05;
              color += texture2D(screen, imageUv + vec2(offset.x, -offset.y)) * 0.05;
              color += texture2D(screen, imageUv + vec2(-offset.x, offset.y)) * 0.05;
            }
            float backgroundPixel = 1.0 - smoothstep(0.003, 0.018, distance(color.rgb, terminalBackground));
            color.rgb = mix(color.rgb, screenBackground, backgroundPixel * 0.90);
            float grille = mix(1.0, 0.992 + 0.008 * cos(vUv.x * resolution.x * 2.094), opticalEffects);
            float vignette = mix(1.0, 1.0 - 0.10 * smoothstep(0.48, 0.90, length((vUv - 0.5) * vec2(1.0, 0.78))), opticalEffects);
            float edgeDistance = min(min(vUv.x, 1.0 - vUv.x), min(vUv.y, 1.0 - vUv.y));
            float edgeLine = 1.0 - smoothstep(0.002, 0.008, edgeDistance);
            color.rgb = mix(color.rgb, vec3(0.045, 0.065, 0.085), edgeLine * 0.65);
            gl_FragColor = vec4(color.rgb * grille * vignette, color.a);
            #include <tonemapping_fragment>
            #include <colorspace_fragment>
          }
        `,
        toneMapped: false
      });
    }

    function screenCoordinates(event) {
      const bounds = canvas.getBoundingClientRect();
      if (bounds.width <= 0 || bounds.height <= 0) return null;
      pointer.set(
        Math.max(-1, Math.min(1, ((event.clientX - bounds.left) / bounds.width) * 2 - 1)),
        Math.max(-1, Math.min(1, -((event.clientY - bounds.top) / bounds.height) * 2 + 1))
      );
      raycaster.setFromCamera(pointer, camera);
      const hit = raycaster.intersectObject(screenMesh, false)[0];
      if (!hit || !hit.uv) return null;
      const imageScale = screenMaterial && screenMaterial.uniforms && screenMaterial.uniforms.imageScale
        ? screenMaterial.uniforms.imageScale.value
        : { x: 1, y: 1 };
      const u = (hit.uv.x - 0.5) / (imageScale.x || 1) + 0.5;
      const v = (hit.uv.y - 0.5) / (imageScale.y || 1) + 0.5;
      hitPoint.set(Math.max(0, Math.min(1, u)), Math.max(0, Math.min(1, v)));
      const targetBounds = terminalScreen.getBoundingClientRect();
      return {
        clientX: targetBounds.left + hitPoint.x * targetBounds.width,
        clientY: targetBounds.top + (1 - hitPoint.y) * targetBounds.height
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

      const gl = rendererContextProbe(THREE, canvas);
      if (!gl) throw new Error('WebGL2 is unavailable');
      const rendererInfo = gl.getExtension('WEBGL_debug_renderer_info');
      if (rendererInfo) {
        const rendererName = gl.getParameter(rendererInfo.UNMASKED_RENDERER_WEBGL);
        if (/swiftshader|software rasterizer|llvmpipe/i.test(rendererName)) {
          throw new Error(`software WebGL renderer detected (${rendererName})`);
        }
      }
      renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: false });
      renderer.outputColorSpace = THREE.SRGBColorSpace;
      renderer.toneMapping = THREE.ACESFilmicToneMapping;
      renderer.toneMappingExposure = 1.08;

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
      textureContext.drawImage(
        screenCanvas,
        viewport.x,
        viewport.y,
        viewport.width,
        viewport.height,
        0,
        0,
        textureCanvas.width,
        textureCanvas.height
      );
      applyScanlines();
      screenTexture = new THREE.CanvasTexture(textureCanvas);
      screenTexture.colorSpace = THREE.SRGBColorSpace;
      screenTexture.minFilter = THREE.LinearFilter;
      screenTexture.magFilter = THREE.LinearFilter;
      screenMaterial = createScreenMaterial();
      screenMaterial.uniforms.resolution.value.set(textureCanvas.width, textureCanvas.height);
      geometry = makeGeometry();

      scene = new THREE.Scene();
      scene.background = new THREE.Color(0x07090e);
      camera = new THREE.PerspectiveCamera(31, 1, 0.1, 50);

      screenMesh = new THREE.Mesh(geometry, screenMaterial);
      scene.add(screenMesh);

      if (!flat) glassMaterial = new THREE.MeshPhysicalMaterial({
        color: 0xeaf3ff,
        roughness: 0.16,
        metalness: 0,
        transmission: 0.045,
        thickness: 0.04,
        ior: 1.48,
        clearcoat: 0.35,
        clearcoatRoughness: 0.22,
        transparent: true,
        opacity: 0.10,
        side: THREE.DoubleSide,
        depthWrite: false
      });
      if (glassMaterial) {
        glassMesh = new THREE.Mesh(geometry, glassMaterial);
        glassMesh.position.z = 0.014;
        scene.add(glassMesh);
      }

      if (!flat) reflectionMaterial = new THREE.ShaderMaterial({
        transparent: true,
        depthWrite: false,
        side: THREE.DoubleSide,
        toneMapped: false,
        blending: THREE.AdditiveBlending,
        vertexShader: `
          varying vec2 vUv;
          varying vec3 vViewPosition;
          varying vec3 vNormal;
          void main() {
            vUv = uv;
            vec4 viewPosition = modelViewMatrix * vec4(position, 1.0);
            vViewPosition = -viewPosition.xyz;
            vNormal = normalize(normalMatrix * normal);
            gl_Position = projectionMatrix * viewPosition;
          }
        `,
        fragmentShader: `
          varying vec2 vUv;
          varying vec3 vViewPosition;
          varying vec3 vNormal;
          void main() {
            float facing = max(dot(normalize(vNormal), normalize(vViewPosition)), 0.0);
            float fresnel = pow(1.0 - facing, 2.0);
            float streakAxis = vUv.x + 0.34 * (1.0 - vUv.y);
              float streak = exp(-pow((streakAxis - 0.50) / 0.12, 2.0)) *
                smoothstep(0.08, 0.28, vUv.y) *
                (1.0 - smoothstep(0.92, 0.99, vUv.y));
              float bloom = exp(-pow((vUv.x - 0.18) / 0.22, 2.0) -
                pow((vUv.y - 0.82) / 0.22, 2.0));
                float alpha = clamp(streak * 0.34 + bloom * 0.15 + fresnel * 0.10, 0.0, 0.36);
            gl_FragColor = vec4(0.78, 0.88, 1.0, alpha);
          }
        `
      });
      if (reflectionMaterial) {
        reflectionMesh = new THREE.Mesh(geometry, reflectionMaterial);
        reflectionMesh.position.z = 0.024;
        reflectionMesh.renderOrder = 2;
        scene.add(reflectionMesh);
      }

      const ambient = new THREE.AmbientLight(0x9db4d0, 0.30);
      const key = new THREE.DirectionalLight(0xd6e4ff, 0.50);
      key.position.set(-1.7, 1.8, 2.2);
      scene.add(ambient, key);

      if (!terminal._core || !terminal._core._renderService ||
          typeof terminal._core._renderService.onRender !== 'function') {
        throw new Error('This xterm.js version does not expose screen redraw notifications required by the CRT surface');
      }
      renderSubscription = terminal._core._renderService.onRender(requestFrame);
      contextLossSubscription = addon.onContextLoss(() => {
        fallback(new Error('xterm WebGL context was lost'));
      });

      canvas.addEventListener('mousemove', onCanvasMove);
      canvas.addEventListener('mousedown', onCanvasDown);
      canvas.addEventListener('click', forwardMouseEvent);
      canvas.addEventListener('dblclick', forwardMouseEvent);
      canvas.addEventListener('wheel', forwardMouseEvent, { passive: false });
      canvas.addEventListener('contextmenu', forwardMouseEvent);
      global.addEventListener('mousemove', onWindowMove);
      global.addEventListener('mouseup', onWindowUp);
      resizeObserver = new global.ResizeObserver(resize);
      resizeObserver.observe(root);
      updateScreenDimensions(stage.clientWidth / stage.clientHeight);
      resize();
      requestFrame();
    } catch (error) {
      fallback(error);
    }

    return {
      fitTerminal,
      resize,
      fallback,
      isFallback: () => usingFallback,
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

  function rendererContextProbe(THREE, canvas) {
    if (typeof THREE.WebGLRenderer !== 'function') return null;
    return canvas.getContext('webgl2');
  }

  function calculateGrid(width, height) {
    const viewportAspect = width > 0 && height > 0 ? width / height : 1.6;
    const aspect = Number.isFinite(viewportAspect) ? viewportAspect : 1.6;
    const targetCells = 72 * 20;
    const referenceAspect = 1.6;
    const referenceGridAspect = 72 / 20;
    const requestedGridAspect = referenceGridAspect * (aspect / referenceAspect);
    const gridAspect = Math.min(72 / 20, Math.max(20 / 72, requestedGridAspect));
    const cols = Math.min(72, Math.max(20, Math.round(Math.sqrt(targetCells * gridAspect))));
    const rows = Math.min(72, Math.max(20, Math.round(targetCells / cols)));
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

  global.KabibiBrowserSuperDemo = Object.freeze({
    start,
    createVirtualKeyboard,
    calculateGrid,
    calculateImageScale,
    calculateCaptureViewport
  });
})(globalThis);
