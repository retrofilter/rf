/* Knowledge-base graph view. term.js owns when the view shows (RFGraph.show /
   RFGraph.hide on view flips); this file owns everything inside it: the
   canvas force layout, pan/zoom/drag, the type legend, and the node detail
   panel. The data arrives whole from GET /ui/graph — properties included —
   so selection, neighbor navigation, and the legend never round-trip.

   The layout is a plain velocity-Verlet force simulation (pairwise repulsion
   with a distance cutoff, springs along edges, weak centering gravity) run
   inside the render loop with a decaying alpha; the loop stops when the
   layout cools and restarts on drag. Node positions persist at module scope
   keyed by id, so leaving and re-entering the view — or a refetch — never
   reshuffles the picture. */

(() => {
  'use strict';

  const $ = (sel) => document.querySelector(sel);

  /* Canvas colors per console theme (the canvas can't read CSS tokens, so the
     ramps live here, keyed off the same data-theme attribute style.css and
     term.js switch on). The categorical node-type palette is the ANSI hues of
     each theme — One Light on #FAFAFA, One Dark on #282C34 — in this exact
     order, validated CVD-safe for adjacent pairs against their surface
     (dataviz six-checks; red and green must not sit adjacent). The core
     vocabulary keeps stable slots (project=blue, task=amber, note=purple);
     further types
     take the remaining slots alphabetically, and past six fold into gray. */
  const THEMES = {
    light: {
      palette: ['#4078F2', '#C18401', '#A626A4', '#50A14F', '#0184BC', '#E45649'],
      other: '#A0A1A7',
      surface: '#FAFAFA',
      ink: '#383A42',
      muted: '#696C77',
      edge: '#DEDEDA',
      edgeHi: '#696C77',
    },
    dark: {
      palette: ['#61AFEF', '#E5C07B', '#C678DD', '#98C379', '#56B6C2', '#E06C75'],
      other: '#5C6370',
      surface: '#282C34',
      ink: '#ABB2BF',
      muted: '#7F848E',
      edge: '#3E4451',
      edgeHi: '#7F848E',
    },
  };
  const theme = () => THEMES[document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'];

  const PINNED = ['project', 'task', 'note'];

  const REPULSION = 1600;
  const CUTOFF2 = 260 * 260;
  const SPRING_LEN = 80;
  const SPRING_K = 0.05;
  const GRAVITY = 0.012;
  const DAMPING = 0.6;
  const ALPHA_DECAY = 0.97;
  const ALPHA_MIN = 0.02;
  const LABEL_MAX = 30; // drawn beside the node; the panel has the full text

  let canvas = null;
  let ctx = null;
  let host = null;

  let nodes = [];
  let edges = [];
  let byId = new Map();
  let colorOf = new Map(); // type -> palette slot (resolved per theme at draw time)
  let hiddenTypes = new Set();
  let truncated = false;

  // Positions outlive rebuilds: rebuilt nodes are re-registered here, so the
  // map always holds the live objects and a refetch starts from where the
  // layout settled.
  const pos = new Map();

  const cam = { x: 0, y: 0, scale: 1 };
  let alpha = 0;
  let raf = null;
  let showing = false;
  let userMoved = false; // a pan/zoom/drag means never auto-fit again
  let fitOnCool = false;

  let hoverNode = null;
  let selectedNode = null;
  let dragNode = null;

  /* ---- data ---- */

  function show() {
    if (!canvas) boot();
    showing = true;
    resize();
    fetch('/ui/graph')
      .then((res) => {
        if (!res.ok) throw new Error(res.statusText);
        return res.json();
      })
      .then(build)
      .catch(() => {
        $('#graph-stats').textContent = 'failed to load graph';
      });
    kick();
  }

  function hide() {
    showing = false;
    if (raf) {
      cancelAnimationFrame(raf);
      raf = null;
    }
  }

  function build(data) {
    truncated = data.truncated;
    nodes = data.nodes.map((n, i) => {
      const prev = pos.get(n.id);
      // Fresh nodes seed on a spiral — deterministic, so the first paint (and
      // webshot) is the same picture every time.
      const r = 26 * Math.sqrt(i);
      const a = i * 2.399963;
      const node = {
        id: n.id,
        type: n.type,
        label: n.label,
        props: n.props || {},
        x: prev ? prev.x : r * Math.cos(a),
        y: prev ? prev.y : r * Math.sin(a),
        vx: 0,
        vy: 0,
        deg: 0,
        out: [],
        in: [],
      };
      pos.set(n.id, node);
      return node;
    });
    byId = new Map(nodes.map((n) => [n.id, n]));
    edges = [];
    for (const e of data.edges) {
      const a = byId.get(e.source);
      const b = byId.get(e.target);
      if (!a || !b) continue;
      const edge = { a, b, type: e.type };
      a.out.push(edge);
      b.in.push(edge);
      a.deg++;
      b.deg++;
      edges.push(edge);
    }
    assignColors();
    renderLegend();
    renderStats();
    $('#graph-empty').hidden = nodes.length > 0;
    $('#graph-hint').hidden = nodes.length === 0;

    // Reselect across refetches; a deleted node closes the panel.
    selectedNode = selectedNode ? byId.get(selectedNode.id) || null : null;
    renderSide();

    alpha = 1;
    if (!userMoved) {
      fit();
      fitOnCool = true;
    }
    kick();
  }

  function assignColors() {
    const types = [...new Set(nodes.map((n) => n.type))];
    const order = PINNED.filter((t) => types.includes(t)).concat(
      types.filter((t) => !PINNED.includes(t)).sort()
    );
    colorOf = new Map(order.map((t, i) => [t, i]));
  }

  function colorFor(type) {
    const t = theme();
    const i = colorOf.get(type);
    return i === undefined || i >= t.palette.length ? t.other : t.palette[i];
  }
  const nodeHidden = (n) => hiddenTypes.has(n.type);
  const radiusOf = (n) => 4 + Math.min(8, Math.sqrt(n.deg) * 1.6);

  /* ---- chrome (legend, stats, detail panel) ---- */

  function renderStats() {
    let text = `${nodes.length} nodes · ${edges.length} edges`;
    if (truncated) text += ' · truncated';
    $('#graph-stats').textContent = text;
  }

  function renderLegend() {
    const legend = $('#graph-legend');
    legend.replaceChildren();
    const countBy = new Map();
    for (const n of nodes) countBy.set(n.type, (countBy.get(n.type) || 0) + 1);
    for (const type of colorOf.keys()) {
      const chip = el('div', 'gl-chip');
      if (hiddenTypes.has(type)) chip.classList.add('off');
      const dot = el('span', 'gl-dot');
      dot.style.background = colorFor(type);
      chip.append(dot, el('span', '', type || '(untyped)'), el('span', 'gl-count', String(countBy.get(type) || 0)));
      chip.title = 'toggle visibility';
      chip.addEventListener('click', () => {
        if (hiddenTypes.has(type)) hiddenTypes.delete(type);
        else hiddenTypes.add(type);
        chip.classList.toggle('off');
        render();
      });
      legend.append(chip);
    }
  }

  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  // The detail panel is DOM-built with textContent throughout — node
  // properties are user data and must never reach innerHTML.
  function renderSide() {
    const side = $('#graph-side');
    side.hidden = !selectedNode;
    side.replaceChildren();
    if (!selectedNode) return;
    const n = selectedNode;

    const head = el('div', 'gs-head');
    const dot = el('span', 'gl-dot');
    dot.style.background = colorFor(n.type);
    head.append(dot, el('span', 'gs-type', n.type || '(untyped)'), el('span', 'gs-id', '#' + n.id));
    side.append(head, el('div', 'gs-label', n.label));

    const props = Object.keys(n.props)
      .filter((k) => k !== 'type')
      .sort();
    if (props.length) {
      side.append(el('div', 'gs-section', 'PROPERTIES'));
      const grid = el('div', 'gs-props');
      for (const k of props) {
        const v = n.props[k];
        grid.append(el('span', 'gs-key', k), el('span', 'gs-val', typeof v === 'string' ? v : JSON.stringify(v)));
      }
      side.append(grid);
    }

    side.append(el('div', 'gs-section', 'EDGES'));
    if (!n.out.length && !n.in.length) {
      side.append(el('div', 'gs-none', 'no edges'));
      return;
    }
    const edgeRow = (label, neighbor) => {
      const row = el('div', 'gs-edge');
      row.append(el('span', 'gs-etype', label), el('span', 'gs-etext', neighbor.label));
      row.title = neighbor.label;
      row.addEventListener('click', () => {
        select(neighbor);
        cam.x = neighbor.x;
        cam.y = neighbor.y;
        render();
      });
      return row;
    };
    for (const e of n.out) side.append(edgeRow(`${e.type} →`, e.b));
    for (const e of n.in) side.append(edgeRow(`← ${e.type}`, e.a));
  }

  function select(node) {
    selectedNode = node;
    renderSide();
    render();
  }

  /* ---- simulation ---- */

  function tick() {
    const k = alpha;
    for (let i = 0; i < nodes.length; i++) {
      const a = nodes[i];
      for (let j = i + 1; j < nodes.length; j++) {
        const b = nodes[j];
        const dx = a.x - b.x;
        const dy = a.y - b.y;
        const d2 = dx * dx + dy * dy + 0.01;
        if (d2 > CUTOFF2) continue;
        const f = (REPULSION / d2) * k;
        a.vx += dx * f;
        a.vy += dy * f;
        b.vx -= dx * f;
        b.vy -= dy * f;
      }
    }
    for (const e of edges) {
      const dx = e.b.x - e.a.x;
      const dy = e.b.y - e.a.y;
      const d = Math.sqrt(dx * dx + dy * dy) + 0.01;
      const f = ((d - SPRING_LEN) * SPRING_K * k) / d;
      e.a.vx += dx * f;
      e.a.vy += dy * f;
      e.b.vx -= dx * f;
      e.b.vy -= dy * f;
    }
    for (const n of nodes) {
      n.vx -= n.x * GRAVITY * k;
      n.vy -= n.y * GRAVITY * k;
      if (n === dragNode) continue;
      n.vx *= DAMPING;
      n.vy *= DAMPING;
      n.x += n.vx;
      n.y += n.vy;
    }
    alpha *= ALPHA_DECAY;
  }

  function kick() {
    if (raf || !showing) return;
    const loop = () => {
      raf = null;
      if (!showing) return;
      if (alpha > ALPHA_MIN) {
        tick();
        // Track the expanding layout with the camera until the user takes
        // over — a one-shot fit would leave the cooling graph off-canvas.
        if (fitOnCool && !userMoved) fit();
        raf = requestAnimationFrame(loop);
      } else if (fitOnCool) {
        if (!userMoved) fit();
        fitOnCool = false;
      }
      render();
    };
    raf = requestAnimationFrame(loop);
  }

  function reheat(a) {
    alpha = Math.max(alpha, a);
    kick();
  }

  /* ---- projection ---- */

  const width = () => canvas.clientWidth;
  const height = () => canvas.clientHeight;
  const toScreenX = (x) => width() / 2 + (x - cam.x) * cam.scale;
  const toScreenY = (y) => height() / 2 + (y - cam.y) * cam.scale;
  const toWorldX = (sx) => (sx - width() / 2) / cam.scale + cam.x;
  const toWorldY = (sy) => (sy - height() / 2) / cam.scale + cam.y;

  function fit() {
    const vis = nodes.filter((n) => !nodeHidden(n));
    if (!vis.length || !width() || !height()) return;
    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    for (const n of vis) {
      minX = Math.min(minX, n.x);
      maxX = Math.max(maxX, n.x);
      minY = Math.min(minY, n.y);
      maxY = Math.max(maxY, n.y);
    }
    // Labels hang off a node's right side, so the fitted frame needs extra
    // room there or the rightmost labels clip at the panel edge.
    const mLeft = 70, mRight = nodes.length <= 250 ? 220 : 70, mY = 60;
    cam.scale = Math.min(
      1.4,
      (width() - mLeft - mRight) / Math.max(1, maxX - minX),
      (height() - mY * 2) / Math.max(1, maxY - minY)
    );
    cam.scale = Math.max(cam.scale, 0.12);
    cam.x = (minX + maxX) / 2 + (mRight - mLeft) / (2 * cam.scale);
    cam.y = (minY + maxY) / 2;
  }

  /* ---- rendering ---- */

  function resize() {
    if (!host.clientWidth || !host.clientHeight) return; // hidden view
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.round(host.clientWidth * dpr);
    canvas.height = Math.round(host.clientHeight * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    // The host resizing (detail panel opening, window resize) refits the
    // frame as long as the camera is still automatic.
    if (!userMoved) fit();
    render();
  }

  function render() {
    if (!ctx || !width() || !height()) return;
    const th = theme();
    ctx.fillStyle = th.surface;
    ctx.fillRect(0, 0, width(), height());

    const focus = hoverNode && !nodeHidden(hoverNode) ? hoverNode : selectedNode;
    const neighbors = new Set();
    if (focus) {
      for (const e of focus.out) neighbors.add(e.b);
      for (const e of focus.in) neighbors.add(e.a);
    }

    ctx.lineWidth = 1;
    for (const e of edges) {
      if (nodeHidden(e.a) || nodeHidden(e.b)) continue;
      const hi = focus && (e.a === focus || e.b === focus);
      ctx.strokeStyle = hi ? th.edgeHi : th.edge;
      ctx.beginPath();
      ctx.moveTo(toScreenX(e.a.x), toScreenY(e.a.y));
      ctx.lineTo(toScreenX(e.b.x), toScreenY(e.b.y));
      ctx.stroke();
      if (hi) arrowhead(e);
    }

    const labelAll = nodes.length <= 250 && cam.scale >= 0.6;
    for (const n of nodes) {
      if (nodeHidden(n)) continue;
      const sx = toScreenX(n.x);
      const sy = toScreenY(n.y);
      const r = Math.max(2.5, radiusOf(n) * Math.min(cam.scale, 1.4));
      ctx.beginPath();
      ctx.arc(sx, sy, r, 0, Math.PI * 2);
      ctx.fillStyle = colorFor(n.type);
      ctx.fill();
      // The 2px surface ring keeps touching marks separable (and is the
      // palette's secondary encoding at small sizes).
      ctx.lineWidth = n === selectedNode ? 2 : 1.5;
      ctx.strokeStyle = n === selectedNode ? th.ink : th.surface;
      ctx.stroke();

      if (labelAll || n === focus || (focus && neighbors.has(n))) {
        const text = n.label.length > LABEL_MAX ? n.label.slice(0, LABEL_MAX) + '…' : n.label;
        ctx.font = "11px 'Hack', ui-monospace, monospace";
        ctx.textBaseline = 'middle';
        ctx.fillStyle = n === focus ? th.ink : th.muted;
        ctx.fillText(text, sx + r + 5, sy);
      }
    }
  }

  // A small arrowhead at the target end of a highlighted edge — direction
  // stays out of the resting picture (clutter) but reads on focus.
  function arrowhead(e) {
    const x1 = toScreenX(e.a.x), y1 = toScreenY(e.a.y);
    const x2 = toScreenX(e.b.x), y2 = toScreenY(e.b.y);
    const d = Math.hypot(x2 - x1, y2 - y1);
    if (d < 12) return;
    const ux = (x2 - x1) / d, uy = (y2 - y1) / d;
    const rb = Math.max(2.5, radiusOf(e.b) * Math.min(cam.scale, 1.4));
    const tx = x2 - ux * (rb + 2), ty = y2 - uy * (rb + 2);
    ctx.beginPath();
    ctx.moveTo(tx, ty);
    ctx.lineTo(tx - ux * 6 - uy * 3.2, ty - uy * 6 + ux * 3.2);
    ctx.lineTo(tx - ux * 6 + uy * 3.2, ty - uy * 6 - ux * 3.2);
    ctx.closePath();
    ctx.fillStyle = theme().edgeHi;
    ctx.fill();
  }

  /* ---- interaction: hover, click-select, node drag, pan, wheel zoom ---- */

  function pick(sx, sy) {
    for (let i = nodes.length - 1; i >= 0; i--) {
      const n = nodes[i];
      if (nodeHidden(n)) continue;
      const r = Math.max(2.5, radiusOf(n) * Math.min(cam.scale, 1.4)) + 3;
      const dx = toScreenX(n.x) - sx;
      const dy = toScreenY(n.y) - sy;
      if (dx * dx + dy * dy <= r * r) return n;
    }
    return null;
  }

  function boot() {
    host = $('#graph-canvas-host');
    canvas = $('#graph-canvas');
    ctx = canvas.getContext('2d');

    new ResizeObserver(() => resize()).observe(host);

    let down = null; // {sx, sy, camX, camY, moved}

    canvas.addEventListener('pointerdown', (ev) => {
      canvas.setPointerCapture(ev.pointerId);
      down = { sx: ev.offsetX, sy: ev.offsetY, camX: cam.x, camY: cam.y, moved: false };
      dragNode = pick(ev.offsetX, ev.offsetY);
    });

    canvas.addEventListener('pointermove', (ev) => {
      if (!down) {
        const over = pick(ev.offsetX, ev.offsetY);
        if (over !== hoverNode) {
          hoverNode = over;
          canvas.style.cursor = over ? 'pointer' : 'default';
          render();
        }
        return;
      }
      if (Math.hypot(ev.offsetX - down.sx, ev.offsetY - down.sy) > 4) down.moved = true;
      if (dragNode) {
        dragNode.x = toWorldX(ev.offsetX);
        dragNode.y = toWorldY(ev.offsetY);
        dragNode.vx = 0;
        dragNode.vy = 0;
        userMoved = true;
        reheat(0.3);
      } else if (down.moved) {
        cam.x = down.camX - (ev.offsetX - down.sx) / cam.scale;
        cam.y = down.camY - (ev.offsetY - down.sy) / cam.scale;
        userMoved = true;
        render();
      }
    });

    canvas.addEventListener('pointerup', (ev) => {
      if (down && !down.moved) select(pick(ev.offsetX, ev.offsetY));
      down = null;
      dragNode = null;
    });

    canvas.addEventListener('wheel', (ev) => {
      ev.preventDefault();
      const wx = toWorldX(ev.offsetX);
      const wy = toWorldY(ev.offsetY);
      cam.scale = Math.min(5, Math.max(0.12, cam.scale * Math.exp(-ev.deltaY * 0.0015)));
      // Keep the world point under the cursor fixed through the zoom.
      cam.x = wx - (ev.offsetX - width() / 2) / cam.scale;
      cam.y = wy - (ev.offsetY - height() / 2) / cam.scale;
      userMoved = true;
      render();
    }, { passive: false });
  }

  // A theme flip (term.js dispatches rf:theme on body) recolors what's
  // already drawn: the legend and detail-panel dots plus the canvas. Before
  // the first show there's nothing to repaint and every call no-ops.
  document.body.addEventListener('rf:theme', () => {
    if (!ctx) return;
    renderLegend();
    renderSide();
    render();
  });

  window.RFGraph = {
    show,
    hide,
    // For scripts (webshot) and the curious: select a node by id, list ids —
    // optionally of one type.
    select: (id) => select(byId.get(id) || null),
    ids: (type) => nodes.filter((n) => type === undefined || n.type === type).map((n) => n.id),
  };
})();
