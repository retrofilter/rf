/* Retrofilter Console terminal glue. Everything renderable is server-side
   (templ fragments swapped by htmx — see ui.templ); this file owns only what
   a server can't: the xterm instance, its websocket, and the two bits of
   client state the poll echoes back (RF.active, RF.view). One xterm lives at
   module scope for the life of the page: selecting a session never rebuilds
   the terminal, it just closes the old websocket, resets the buffer and
   attaches the new stream — the server replays its ring buffer first, so the
   recent screen repaints, and from there xterm owns scrollback natively.

   The htmx contract: the poll and spawn responses carry HX-Trigger events —
   rf:select {id} (attach this session), rf:shownew (open the naming view),
   rf:detached (the active session is gone) — and this file triggers
   rf:refresh on the body when client state changes so the rail re-renders
   without waiting out the 2s poll. */

(() => {
  'use strict';

  const $ = (sel) => document.querySelector(sel);

  // Client state the server renders against; htmx sends it with every poll
  // (hx-vals on #session-list).
  const RF = {
    active: null, // session id — the identity everywhere
    view: 'terminal', // 'terminal' | 'new' (name-a-session) | 'overview' (projects & tasks) | 'graph' (knowledge-base)
    graphOpen: false, // the graph-view tab exists (independent of it being selected)
  };
  window.RF = RF;

  // The xterm ANSI ramps, one per console theme: One Light (Atom) on #FAFAFA
  // and One Dark on #282C34. Each background must match the theme's
  // --terminal in style.css or the terminal rectangle shows against the
  // panel padding. The active theme lives on <html> as data-theme (absent =
  // light, the default), set before first paint by the head script in
  // ui.templ and flipped by the status bar's sun/moon toggle below.
  const THEMES = {
    light: {
      background: '#FAFAFA',
      foreground: '#383A42',
      cursor: '#383A42',
      cursorAccent: '#FAFAFA',
      selectionBackground: '#DCE2EA',
      black: '#383A42',
      red: '#E45649',
      green: '#50A14F',
      yellow: '#C18401',
      blue: '#4078F2',
      magenta: '#A626A4',
      cyan: '#0184BC',
      white: '#A0A1A7',
      brightBlack: '#696C77',
      brightRed: '#CA1243',
      brightGreen: '#50A14F',
      brightYellow: '#C18401',
      brightBlue: '#4078F2',
      brightMagenta: '#A626A4',
      brightCyan: '#0184BC',
      brightWhite: '#FFFFFF',
    },
    dark: {
      background: '#282C34',
      foreground: '#ABB2BF',
      cursor: '#ABB2BF',
      cursorAccent: '#282C34',
      selectionBackground: '#3E4451',
      black: '#282C34',
      red: '#E06C75',
      green: '#98C379',
      yellow: '#E5C07B',
      blue: '#61AFEF',
      magenta: '#C678DD',
      cyan: '#56B6C2',
      white: '#ABB2BF',
      brightBlack: '#5C6370',
      brightRed: '#E06C75',
      brightGreen: '#98C379',
      brightYellow: '#E5C07B',
      brightBlue: '#61AFEF',
      brightMagenta: '#C678DD',
      brightCyan: '#56B6C2',
      brightWhite: '#FFFFFF',
    },
  };

  const themeName = () => (document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light');

  // Flip the attribute (CSS re-tokens), persist, re-skin the live terminal,
  // and tell the graph renderer (rf:theme) so an open canvas repaints.
  function toggleTheme() {
    const next = themeName() === 'dark' ? 'light' : 'dark';
    if (next === 'dark') document.documentElement.dataset.theme = 'dark';
    else delete document.documentElement.dataset.theme;
    localStorage.setItem('rf-theme', next);
    term.options.theme = THEMES[next];
    document.body.dispatchEvent(new CustomEvent('rf:theme'));
  }

  let term = null;
  let fit = null;
  let ws = null;
  const encoder = new TextEncoder();

  function boot() {
    term = new Terminal({
      theme: THEMES[themeName()],
      // 'Hack' is the bundled webfont and wins per-glyph; the Nerd Font
      // entry has no @font-face, so it resolves only where the patched font
      // is installed locally — filling in PUA icon glyphs (vim devicons,
      // prompt segments) that plain Hack lacks. Metrics-identical, so cell
      // measurement is unaffected either way.
      fontFamily: "'Hack', 'Hack Nerd Font Mono', ui-monospace, monospace",
      fontSize: 13,
      cursorBlink: false,
      scrollback: 10000,
    });
    // The console's cursor never blinks. Applications re-enable blinking via
    // DECSCUSR (CSI Ps SP q) — rf's readline emits "ESC[0 q" on every prompt
    // refresh, and per the VT spec 0/1 mean *blinking* block, which xterm.js
    // implements literally (desktop terminals read 0 as "my default", which
    // is why this only flashed in the web console). Swallow the sequence,
    // honor the requested shape, drop the blink.
    term.parser.registerCsiHandler({ intermediates: ' ', final: 'q' }, (params) => {
      const style = { 0: 'block', 1: 'block', 2: 'block', 3: 'underline', 4: 'underline', 5: 'bar', 6: 'bar' }[
        params[0] || 0
      ];
      if (style) term.options.cursorStyle = style;
      term.options.cursorBlink = false;
      return true;
    });

    fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open($('#term-mount'));
    fit.fit();

    term.onData((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(encoder.encode(data));
      }
    });

    // Fit on host resize: dedupe on rounded size and fit inside rAF, or the
    // fit's own layout write re-triggers the observer (resize-loop error).
    let lastSize = '';
    const host = $('#term-mount');
    const ro = new ResizeObserver(() => {
      if (!host.clientWidth || !host.clientHeight) return; // hidden behind the new-session view
      const key = `${Math.round(host.clientWidth)}x${Math.round(host.clientHeight)}`;
      if (key === lastSize) return;
      lastSize = key;
      requestAnimationFrame(() => {
        fit.fit();
        sendResize();
      });
    });
    ro.observe(host);

    /* ---- rail and tab clicks (delegated — htmx swaps the rows) ---- */

    document.addEventListener('click', (ev) => {
      // Close buttons first — they sit inside the tabs they close, so they
      // must win over the tab's own select/show handling below.
      const x = ev.target.closest('.tab-x');
      if (x) {
        const tab = x.closest('.tab');
        if (!tab) return;
        if (tab.dataset.sessionId) {
          // Hang up the session — the mouse spelling of Ctrl-D. The fleet
          // change comes back through the poll; nothing to swap here.
          htmx.ajax('POST', '/ui/close', { values: { id: tab.dataset.sessionId }, swap: 'none' });
        } else if ('graph' in tab.dataset) {
          closeGraph();
        } else if ('pending' in tab.dataset) {
          cancelNew();
        }
        return;
      }
      const row = ev.target.closest('[data-session-id]');
      if (row) {
        select(row.dataset.sessionId);
        return;
      }
      if (ev.target.closest('[data-overview]')) {
        showOverview();
        return;
      }
      if (ev.target.closest('[data-graph]')) {
        showGraph();
        return;
      }
      const ov = ev.target.closest('.ov-row');
      if (ov) {
        activateRow(ov);
        return;
      }
      if (ev.target.closest('[data-pending]')) $('#nv-name').focus();
    });

    $('#tab-new').addEventListener('click', showNewView);
    $('#graph-open').addEventListener('click', showGraph);
    $('#theme-toggle').addEventListener('click', toggleTheme);

    // ⌘K opens the new-session view from anywhere (the browser lets pages
    // claim it, unlike ⌘T/⌘N). Plain Ctrl+K only counts outside the
    // terminal — inside it stays rf's suggest-command / readline kill-line.
    document.addEventListener('keydown', (ev) => {
      if (ev.key !== 'k') return;
      const inTerminal = $('#term-host').contains(ev.target);
      if (ev.metaKey || (ev.ctrlKey && !inTerminal)) {
        ev.preventDefault();
        if (RF.view === 'new') $('#nv-name').focus();
        else showNewView();
      }
    });

    /* ---- new-session view (Enter submits the htmx form natively) ---- */

    // The input grows with its content so the block cursor sits right after
    // the last typed character, terminal-style.
    $('#nv-name').addEventListener('input', () => {
      $('#nv-name').style.width = `${$('#nv-name').value.length}ch`;
    });
    $('#nv-name').addEventListener('keydown', (ev) => {
      if (ev.key === 'Escape') cancelNew();
      // Enter with an empty name must not round-trip a validation error.
      if (ev.key === 'Enter' && !$('#nv-name').value.trim()) ev.preventDefault();
    });

    /* ---- overview keyboard: up/down moves the selection, enter starts ---- */

    document.addEventListener('keydown', (ev) => {
      if (RF.view !== 'overview') return;
      // Collapsed overflow tasks are hidden rows — skip them until their
      // project's "…" row reveals them.
      const rows = [...document.querySelectorAll('#overview-view .ov-row')].filter((r) => !r.hidden);
      let i = rows.findIndex((r) => r.classList.contains('selected'));
      switch (ev.key) {
        case 'ArrowDown':
          i = Math.min(i + 1, rows.length - 1);
          break;
        case 'ArrowUp':
          i = Math.max(i - 1, 0);
          break;
        case 'Enter':
          ev.preventDefault();
          if (i >= 0) activateRow(rows[i]);
          return;
        case 'Escape':
          // Back to the terminal — only somewhere to go when sessions exist.
          if (document.querySelector('[data-session-id]')) {
            hideOverview();
            term.focus();
          }
          return;
        default:
          return;
      }
      ev.preventDefault();
      if (i < 0 || !rows.length) return;
      rows.forEach((r) => r.classList.remove('selected'));
      rows[i].classList.add('selected');
      rows[i].scrollIntoView({ block: 'nearest' });
    });

    // Escape leaves the graph view for the terminal — like the overview,
    // only somewhere to go when sessions exist.
    document.addEventListener('keydown', (ev) => {
      if (RF.view !== 'graph' || ev.key !== 'Escape') return;
      if (document.querySelector('[data-session-id]')) {
        setView('terminal');
        term.focus();
        refresh();
      }
    });

    // A freshly fetched listing starts with the first row selected so enter
    // works immediately.
    document.body.addEventListener('htmx:afterSwap', (ev) => {
      if (ev.detail.target.id !== 'ov-body') return;
      const first = document.querySelector('#overview-view .ov-row');
      if (first && !document.querySelector('#overview-view .ov-row.selected')) {
        first.classList.add('selected');
      }
    });

    /* ---- htmx glue ---- */

    document.body.addEventListener('rf:select', (ev) => select(ev.detail.id));
    document.body.addEventListener('rf:shownew', showNewView);
    document.body.addEventListener('rf:showoverview', showOverview);
    document.body.addEventListener('rf:detached', () => {
      detach();
      RF.active = null;
      term.reset();
    });
    // Session expired or revoked — a reload of / lands on the sign-in page.
    document.body.addEventListener('htmx:responseError', (ev) => {
      if (ev.detail.xhr.status === 401) location.reload();
    });
    // Spawn failures still render into #nv-error (htmx skips 4xx/5xx swaps
    // by default).
    document.body.addEventListener('htmx:beforeSwap', (ev) => {
      if (ev.detail.requestConfig.path === '/ui/sessions' && ev.detail.xhr.status >= 400) {
        ev.detail.shouldSwap = true;
        ev.detail.isError = false;
      }
    });
  }

  function refresh() {
    if (window.htmx) htmx.trigger(document.body, 'rf:refresh');
  }

  /* ---- panel views: terminal, new-session, overview (mutually exclusive) ---- */

  function setView(view) {
    RF.view = view;
    $('#term-host').hidden = view !== 'terminal';
    $('#new-view').hidden = view !== 'new';
    $('#overview-view').hidden = view !== 'overview';
    $('#graph-view').hidden = view !== 'graph';
    // The graph renderer owns its canvas loop: show (re)fetches and starts
    // it, hide stops it — driven here so every view flip, whatever its
    // trigger, keeps the loop's state right.
    if (window.RFGraph) view === 'graph' ? RFGraph.show() : RFGraph.hide();
  }

  function showNewView() {
    setView('new');
    $('#nv-name').value = '';
    $('#nv-name').style.width = '0ch';
    $('#nv-error').textContent = '';
    $('#nv-name').focus();
    refresh();
  }

  function hideNewView() {
    setView('terminal');
    refresh();
  }

  function showOverview() {
    if (RF.view === 'overview') return; // the empty-fleet poll re-triggers
    setView('overview');
    // Fetch a fresh listing each time the view is shown (#ov-body's
    // rf:overview trigger) — tasks change while sessions work.
    if (window.htmx) htmx.trigger(document.body, 'rf:overview');
    refresh();
  }

  function hideOverview() {
    setView('terminal');
    refresh();
  }

  function showGraph() {
    RF.graphOpen = true;
    if (RF.view === 'graph') return;
    setView('graph');
    refresh();
  }

  // Closing the graph tab (its ×) is distinct from leaving the view (Esc, or
  // selecting another tab): the tab disappears, and if it had the panel the
  // browser falls back the same way a dead session does — the terminal when
  // sessions exist, the overview when none do.
  function closeGraph() {
    RF.graphOpen = false;
    if (RF.view !== 'graph') {
      refresh();
      return;
    }
    if (document.querySelector('[data-session-id]')) {
      setView('terminal');
      term.focus();
      refresh();
    } else {
      showOverview();
    }
  }

  // cancelNew abandons the naming view — Esc and the pending tab's × share
  // it: back to the terminal when sessions exist, the overview when none do.
  function cancelNew() {
    if (document.querySelector('[data-session-id]')) {
      hideNewView();
      term.focus();
    } else {
      showOverview();
    }
  }

  // activateRow is enter/click on an overview row: the "…" overflow row
  // expands its project's hidden tasks in place; anything else starts a
  // session. The start response carries rf:select for the spawned-or-found
  // session; htmx fires it and the regular attach path takes over.
  function activateRow(row) {
    if ('ovMore' in row.dataset) {
      expandMore(row);
      return;
    }
    htmx.ajax('POST', '/ui/start', {
      values: { kind: row.dataset.ovKind, id: row.dataset.ovId },
      swap: 'none',
    });
  }

  // expandMore reveals a project's collapsed overflow tasks — open and done,
  // the listing already carries them hidden, so no round trip — and hands
  // the selection to the first revealed startable row; when the overflow was
  // completed tasks only, it falls back to the row above.
  function expandMore(row) {
    const revealed = [...row.closest('.ov-project').querySelectorAll('.ov-task[hidden]')];
    revealed.forEach((r) => (r.hidden = false));
    const selected = row.classList.contains('selected');
    let next = revealed.find((r) => r.classList.contains('ov-row'));
    if (!next) {
      const rows = [...document.querySelectorAll('#overview-view .ov-row')];
      next = rows[rows.indexOf(row) - 1];
    }
    row.remove();
    if (selected && next) {
      next.classList.add('selected');
      next.scrollIntoView({ block: 'nearest' });
    }
  }

  /* ---- attach ---- */

  function sendResize() {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ resize: { cols: term.cols, rows: term.rows } }));
    }
  }

  function detach() {
    if (ws) {
      ws.onclose = null;
      ws.close();
      ws = null;
    }
  }

  function select(id) {
    const viewChanged = RF.view !== 'terminal';
    if (viewChanged) setView('terminal');
    const reattach = RF.active === id && (!ws || ws.readyState !== WebSocket.OPEN);
    if (RF.active === id && !reattach) {
      // Already attached (e.g. clicking the current session's tab from the
      // overview): the view flip still has to reach the server now, or the
      // tab highlight waits out the 2s poll.
      if (viewChanged) {
        term.focus();
        refresh();
      }
      return;
    }
    detach();
    RF.active = id;
    term.reset();
    fit.fit();

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    const sock = new WebSocket(
      `${proto}//${location.host}/api/attach/${encodeURIComponent(id)}?cols=${term.cols}&rows=${term.rows}`
    );
    sock.binaryType = 'arraybuffer';
    sock.onmessage = (ev) => {
      if (typeof ev.data === 'string') return; // no server text frames
      term.write(new Uint8Array(ev.data));
    };
    sock.onclose = () => {
      if (RF.active === id) {
        term.write('\r\n\x1b[38;5;243m── detached — click the session to reattach\x1b[0m\r\n');
        // A server-side close usually means the session ended (e.g. Ctrl-D).
        // Poll now so the rail and view react instantly instead of waiting
        // out the 2s tick; the server decides what happens (rf:detached,
        // rf:showoverview, or rf:select of the next session).
        refresh();
      }
    };
    ws = sock;
    term.focus();
    refresh();
  }

  // xterm measures its cell grid from the font at open(); make sure the
  // bundled Hack faces are in before that, or the grid is sized against the
  // fallback mono and glyphs misalign until a refit. load() resolves even
  // when the face is missing, so boot never hangs on it.
  function bootWhenReady() {
    Promise.all([document.fonts.load("13px 'Hack'"), document.fonts.load("bold 13px 'Hack'")])
      .then(boot, boot);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bootWhenReady);
  } else {
    bootWhenReady();
  }
})();
