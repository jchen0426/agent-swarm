// live.js, the board's live refresh (Plan 9a-2, design D1): it polls the
// board's regions, GET <the page's path>/board, and moves each region that
// changed in from the response's own document with adoptNode and
// replaceWith, never from a string. Every word it shows is the server's.
(function () {
  var board = document.getElementById("board"), bar = board && board.firstElementChild;
  var state = $("live-state"), pause = $("live-pause"), summary = $("summary"), list = $("feed-list");
  if (!bar || !state || !pause || !summary || !window.XMLHttpRequest || !document.adoptNode || !board.replaceWith) return;
  // The regions as the server names them (regionNames); the first, the
  // kill switch's header, is sticky, so the scroll is kept by the others.
  var names = ["switch", "standby", "board-header", "summary", "strip", "hypotheses", "feed"];
  var note = state.textContent, etag = board.dataset.etag, order = board.dataset.order, round = summary.dataset.round;
  var pending = {}, paused = false, busy = false, again = false, stopped = false;
  var fails = 0, last = 0, timer = 0, staling = 0;

  function $(id) { return document.getElementById(id); }
  function stale() { var h = $("board-header"); return (h && h.dataset.stale) || ""; }
  // The indicator: live within 10 s of the last success, else the header's
  // "reconnecting, last updated at T".
  function show() { if (!stopped) state.textContent = Date.now() - last <= 10000 ? bar.dataset.live : stale(); }
  function stop(text) { stopped = true; clearTimeout(timer); clearTimeout(staling); state.textContent = text; }
  // No handler throws (R170): a fault reads as a poll's error, the
  // indicator reconnecting, and the next poll is scheduled.
  function guard(f) {
    return function (e) { try { f(e); } catch (err) { last = 0; later(false); } };
  }
  // The next poll after one ended: 5 s after a success, at once if one fell
  // due meanwhile; 10, 20, 40, then 60 s after each error in a row (R159).
  function later(ok) {
    if (ok) { fails = 0; last = Date.now(); clearTimeout(staling); staling = setTimeout(guard(show), 10001); }
    else fails = Math.min(fails + 1, 4);
    var wait = ok ? (again ? 0 : 5000) : Math.min(5000 * Math.pow(2, fails), 60000);
    again = false;
    if (!stopped) { clearTimeout(timer); timer = setTimeout(guard(poll), wait); }
    show();
  }
  // One request at a time, while the page is visible.
  function poll() {
    if (stopped || document.visibilityState !== "visible") return;
    if (busy) { again = true; return; }
    clearTimeout(timer);
    var x = new XMLHttpRequest();
    x.open("GET", location.pathname + "/board" + location.search);
    x.responseType = "document"; x.timeout = 15000;
    if (etag) x.setRequestHeader("If-None-Match", etag);
    x.onloadend = guard(function () { busy = false; later(took(x)); });
    x.send();
    busy = true;
  }
  // took applies an answer, and says whether it was a success.
  function took(x) {
    if (x.status === 304) return true;
    if (x.status === 401) return reload(true);
    if (x.status !== 200) return false;
    var doc = x.response;
    if (!doc || !doc.getElementById) { stop(note); pause.hidden = true; return false; }
    var root = doc.getElementById("board"), fresh = {}, i;
    for (i = 0; i < names.length; i++) {
      fresh[names[i]] = doc.getElementById(names[i]);
      if (!root || !fresh[names[i]] || !$(names[i])) return reload(false);
    }
    var o = root.dataset.order, s = fresh.summary, line = s.querySelector("p");
    if (o && order && below(o, order)) return true; // another instance's lag (re-review N3)
    for (i = 0; i < names.length; i++) take(names[i], fresh[names[i]]);
    // The polite summary, once a round (plan review M10(g)).
    if (s.dataset.round && s.dataset.round !== round && line) { round = s.dataset.round; $("announce").textContent = line.textContent; }
    etag = root.dataset.etag; order = o;
    return true;
  }
  function below(a, b) { var x = a.split("."), y = b.split("."); return +x[0] < +y[0] || +x[1] < +y[1]; }
  // A reload once in 60 s at most (R165, R170), for a 401, so that sign-in
  // begins at the top level, or a region missing; a second, or a storage
  // that throws, stops the polls, reading reconnecting, a 401 showing the
  // sign-in link.
  function reload(signin) {
    var now = Date.now(), may = false;
    try {
      may = !(now - Number(sessionStorage.getItem("live.reloaded")) < 60000);
      if (may) sessionStorage.setItem("live.reloaded", String(now));
    } catch (err) { may = false; }
    if (may) { stopped = true; location.reload(); return false; }
    stop(stale());
    if (signin) $("live-signin").hidden = false;
    return false;
  }
  // A region unchanged drops any copy pending; one changed is moved in, or
  // kept pending, its update control shown, while it holds the focus or a
  // selection, or while updates are paused, which holds all but the header.
  function take(id, copy) {
    var old = $(id), changed = old.dataset.v !== copy.dataset.v;
    var wait = changed && (holds(old) || (paused && id !== "board-header"));
    delete pending[id];
    var c = old.firstElementChild;
    if (c && c.className === "update") c.hidden = !wait;
    if (changed) pending[id] = copy;
    if (changed && !wait) apply(id);
  }
  function holds(region) {
    var s = window.getSelection && getSelection();
    return region.contains(document.activeElement) || !!(s && !s.isCollapsed && s.containsNode && s.containsNode(region, true));
  }
  // apply moves a pending copy in (re-review N6): each <details> open as
  // its old one, by id; the focus back by id; the first region on screen
  // at its offset.
  function apply(id) {
    var old = $(id), copy = pending[id], from = copy.ownerDocument, at = anchor(), a = document.activeElement;
    var focused = a && a.id && old.contains(a) ? a.id : "", ds = old.querySelectorAll("details[id]");
    delete pending[id];
    for (var i = 0; i < ds.length; i++) {
      var d = from.getElementById(ds[i].id);
      if (d && copy.contains(d)) d.open = ds[i].open;
    }
    var n = document.adoptNode(copy);
    old.replaceWith(n);
    var f = focused && $(focused), e = at && $(at.id);
    if (f && n.contains(f)) f.focus();
    if (e) scrollBy(0, e.getBoundingClientRect().top - at.top);
    return n;
  }
  function anchor() {
    for (var i = 1, r; i < names.length; i++) if ((r = $(names[i]).getBoundingClientRect()).bottom > 0) return { id: names[i], top: r.top };
    return null;
  }
  // settle moves in each pending region no longer held.
  function settle() {
    for (var id in pending) if (!holds($(id)) && (!paused || id === "board-header")) apply(id);
  }
  pause.addEventListener("click", guard(function () {
    paused = !paused;
    pause.setAttribute("aria-pressed", String(paused));
    if (!paused) settle();
  }));
  document.addEventListener("click", guard(function (e) {
    var c = e.target, region = c && c.parentNode;
    if (c.className === "update" && region && pending[region.id]) apply(region.id).focus({ preventScroll: true });
  }));
  document.addEventListener("focusout", guard(function () { setTimeout(guard(settle), 0); }));
  document.addEventListener("selectionchange", guard(settle));
  document.addEventListener("visibilitychange", guard(function () {
    if (document.visibilityState === "visible") poll(); else clearTimeout(timer);
  }));
  window.addEventListener("pageshow", guard(function (e) { if (e.persisted) poll(); }));
  pause.hidden = false;
  if (list && window.matchMedia && !matchMedia("(min-width:60em)").matches) list.open = false;
  later(true);
})();
