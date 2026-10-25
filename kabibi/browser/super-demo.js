(function (global) {
  'use strict';

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
      position: 'absolute',
      inset: '0',
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
    root.appendChild(stage);

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
      const scale = calculateImageScale(
        textureCanvas.width,
        textureCanvas.height,
        screenWidth,
        screenHeight
      );
      screenMaterial.uniforms.imageScale.value.set(scale.x, scale.y);
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

    function frameCamera() {
      const halfFov = camera.fov * Math.PI / 360;
      const fitMargin = 1.1;
      const distanceForHeight = screenHeight * fitMargin / (2 * Math.tan(halfFov));
      const distanceForWidth = screenWidth * fitMargin /
        (2 * Math.tan(halfFov) * camera.aspect);
      const distance = Math.max(distanceForHeight, distanceForWidth);
      const scale = distance / cameraOffsetLength;
      camera.position.set(
        cameraTarget.x + cameraOffset.x * scale,
        cameraTarget.y + cameraOffset.y * scale,
        cameraTarget.z + cameraOffset.z * scale
      );
      camera.lookAt(cameraTarget.x, cameraTarget.y, cameraTarget.z);
      if (screenMaterial) {
        screenMaterial.uniforms.focusStart.value = distance - 0.16;
        screenMaterial.uniforms.focusEnd.value = distance + 0.08;
      }
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
          opticalEffects: { value: flat ? 0 : 1 },
          focusStart: { value: 2.5 },
          focusEnd: { value: 2.8 }
        },
        vertexShader: `
          varying vec2 vUv;
          varying float vViewDepth;
          void main() {
            vUv = uv;
            vec4 viewPosition = modelViewMatrix * vec4(position, 1.0);
            vViewDepth = -viewPosition.z;
            gl_Position = projectionMatrix * viewPosition;
          }
        `,
        fragmentShader: `
          uniform sampler2D screen;
          uniform vec2 resolution;
          uniform vec2 imageScale;
          uniform vec3 terminalBackground;
          uniform vec3 screenBackground;
          uniform float opticalEffects;
          uniform float focusStart;
          uniform float focusEnd;
          varying vec2 vUv;
          varying float vViewDepth;
          void main() {
            vec2 imageUv = (vUv - 0.5) / imageScale + 0.5;
            bool outsideImage = any(greaterThan(abs(vUv - 0.5), imageScale * 0.5));
            float nearFocus = 1.0 - smoothstep(focusStart, focusEnd, vViewDepth);
            float lowerRight = smoothstep(0.60, 0.98, vUv.x) *
              (1.0 - smoothstep(0.05, 0.56, vUv.y));
            float blur = clamp(nearFocus * lowerRight * opticalEffects, 0.0, 1.0);
            vec2 texel = 1.0 / max(resolution, vec2(1.0));
            vec2 offset = texel * blur * 1.25;
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
      hitPoint.copy(hit.uv);
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
              float streak = exp(-pow((streakAxis - 0.50) / 0.11, 2.0)) *
                smoothstep(0.08, 0.28, vUv.y) *
                (1.0 - smoothstep(0.92, 0.99, vUv.y));
              float bloom = exp(-pow((vUv.x - 0.18) / 0.22, 2.0) -
                pow((vUv.y - 0.82) / 0.22, 2.0));
              float alpha = clamp(streak * 0.22 + bloom * 0.08 + fresnel * 0.07, 0.0, 0.24);
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
      dispose() {
        if (!active) return;
        active = false;
        removeInputListeners();
        disposeScene();
        if (addon) addon.dispose();
        addon = null;
        stage.remove();
        root.style.opacity = '';
        root.style.pointerEvents = '';
      }
    };
  }

  function sendDisplayTest(host, cols = 72, rows = 20) {
    if (!host || typeof host.pushStdin !== 'function') {
      throw new Error('CRT display test requires a running terminal host');
    }
    const safeCols = Math.max(20, Math.floor(cols));
    const safeRows = Math.max(20, Math.floor(rows));
    const label = `|KABIBI_CRT_0123456789_ABCDEFGHIJKLMNOPQRSTUVWXYZ`.repeat(2)
      .slice(0, Math.max(0, safeCols - 3));
    const values = Array.from({ length: safeRows * 2 - 2 }, (_, index) =>
      String(index + 1).padStart(2, '0')).join(' ');
    const command = `for i in ${values}; do printf '\\033[48;2;10;38;46m\\033[1;36m%s${label}\\033[0m\\n' "$i"; done`;
    host.pushStdin(`${command}${String.fromCharCode(13)}`);
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
    sendDisplayTest,
    calculateGrid,
    calculateImageScale,
    calculateCaptureViewport
  });
})(globalThis);
