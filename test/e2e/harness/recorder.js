(() => {
  const send = (kind, detail) => {
    try { gateRecord(JSON.stringify({kind: kind, t: Date.now(), path: location.pathname, detail: detail})); } catch (e) {}
  };
  const regions = ["switch","standby","board-header","summary","strip","hypotheses","feed"];
  const regionOf = (n) => {
    for (let e = n; e && e.nodeType === 1; e = e.parentElement) if (regions.indexOf(e.id) >= 0) return e.id;
    return "";
  };
  const describe = (n) => {
    if (!n || n.nodeType !== 1) return null;
    let visible = false;
    try { visible = n.matches(":focus-visible"); } catch (e) {}
    return {tag: n.tagName.toLowerCase(), id: n.id || "", cls: typeof n.className === "string" ? n.className : "", region: regionOf(n), focusVisible: visible};
  };
  const watch = (target, options, f) => { try { new MutationObserver(f).observe(target, options); } catch (e) {} };
  let started = false;
  const start = () => {
    if (started) return;
    started = true;
    const board = document.getElementById("board");
    const bar = board && board.firstElementChild;
    const state = document.getElementById("live-state");
    send("start", {
      order: board ? board.getAttribute("data-order") || "" : null,
      etag: board ? board.getAttribute("data-etag") || "" : null,
      bar: state ? state.textContent : null,
      visibility: document.visibilityState
    });
    if (bar && state) watch(bar, {childList: true, characterData: true, subtree: true}, () => send("bar", {text: state.textContent}));
    const announce = document.getElementById("announce");
    let writes = 0;
    if (announce) watch(announce, {childList: true, characterData: true, subtree: true}, (ms) => {
      writes += ms.length;
      send("announce", {writes: writes, text: announce.textContent});
    });
    watch(document.documentElement, {childList: true, subtree: true, attributes: true, attributeFilter: ["data-v"]}, (ms) => {
      for (const m of ms) {
        if (m.type === "attributes") {
          if (regions.indexOf(m.target.id) >= 0) send("region", {region: m.target.id, v: m.target.getAttribute("data-v") || "", how: "in place"});
          continue;
        }
        for (const n of m.addedNodes) {
          if (n.nodeType === 1 && regions.indexOf(n.id) >= 0) send("region", {region: n.id, v: n.getAttribute("data-v") || "", how: "replaced"});
        }
      }
    });
  };
  document.addEventListener("readystatechange", () => { if (document.readyState !== "loading") start(); });
  if (document.readyState !== "loading") start();
  document.addEventListener("visibilitychange", () => send("visibility", {state: document.visibilityState}));
  window.addEventListener("pageshow", (e) => send("pageshow", {persisted: e.persisted}));
  document.addEventListener("focusin", (e) => send("focusin", describe(e.target)));
})();