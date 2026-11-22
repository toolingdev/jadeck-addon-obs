// Audio: one strip per OBS source with sound, on jadeck's shared frame
// kit fader strip (JK.Strip, as the WING and vMix mixers): name, fader
// (OBS's volume in dB), LED meter with peak hold, readout and MUTE.
// Levels come from the add-on's "meters" resource ~12 times a second;
// the widget's state is the fallback.
(function () {
  "use strict";
  var K = window.Kit, JK = window.JK, main = document.getElementById("main"), hdr = null;
  var strips = [], byRef = {}, live = 0, rows = {}, built = "", raf = 0, started = false, shown = 0;
  // OBS's fader runs -100 … 0 dB (and a little above); meters red from -3, amber from -12
  var law = JK.law([[0, -144], [0.0001, -60], [0.2, -40], [0.4, -25], [0.6, -14], [0.8, -6], [0.92, -2], [1, 0]],
    [0, -6, -14, -25, -40, -60, -144], { hot: -3, warm: -12 });

  function ids() {
    var l = K.list(K.settings.strips);
    if (l.length) return l;
    return (Array.isArray(K.state.mixerStrips) ? K.state.mixerStrips : []).map(function (x) { return x.id; });
  }
  function fromState() {
    var by = {};
    (Array.isArray(K.state.mixerStrips) ? K.state.mixerStrips : []).forEach(function (x) { by[x.id] = x; });
    return ids().map(function (id) { var x = by[id]; return x ? Object.assign({ ref: id }, x) : null; }).filter(Boolean);
  }
  function opts() { return { meter: K.bool("meters", true), readout: true, mute: true, fader: K.bool("faders", true) }; }
  function canWrite() { return !K.editing && K.state.connected !== false && !K.state.readOnly; }

  function build(list) {
    var o = opts(), box = document.getElementById("strips");
    if (!box) { main.textContent = ""; box = K.el("div", "jk-strips"); box.id = "strips"; main.appendChild(box); }
    var r = main.getBoundingClientRect();
    var L = JK.stripLayout(r.width, r.height, list.length, { readout: o.readout, mute: o.mute, gap: 8 });
    shown = L.shown;
    var key = list.map(function (s) { return s.ref; }).join("|") + JSON.stringify(o) + L.rows + "x" + L.cols + ":" + L.shown + ":" + Math.round(L.rowH / 20);
    if (key === built) return;
    built = key;
    box.textContent = "";
    rows = {};
    JK.stripGrid(box, L);
    box.style.gap = "8px";
    list.slice(0, L.shown).forEach(function (s) {
      var ref = s.ref;
      rows[ref] = new JK.Strip(box, {
        width: L.colW, height: L.rowH, law: law, fader: o.fader, meter: o.meter, readout: o.readout, mute: o.mute, solo: false, canWrite: canWrite,
        onLevel: function (db) { return K.act("volume", { input: ref, db: db <= -100 ? -100 : db }).catch(function () {}); },
        onMute: function () { var c = byRef[ref] || {}; return K.act("mute", { input: ref, muted: !c.muted }).catch(function () {}); },
      });
    });
  }
  function header(right) {
    if (!hdr) hdr = JK.header({ title: "Audio" });
    hdr.set({ status: K.state.connected === false || K.state.error ? "bad" : "ok", source: K.state.source || "OBS", right: right });
  }
  function update(list) {
    strips = list;
    byRef = {};
    list.forEach(function (s) { byRef[s.ref] = s; });
    if (!list.length) {
      header("");
      built = ""; rows = {};
      return K.empty(main, K.state.connected === false || K.state.error ? (K.state.error || "OBS isn't answering") : "No sources with sound in OBS yet.");
    }
    if (!built) header("");
    build(list);
    header(shown < list.length ? shown + " OF " + list.length + " SOURCES" : list.length + " SOURCE" + (list.length === 1 ? "" : "S"));
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(paint);
  }
  function lvl(a) { var d = K.ampDb(a || 0); return d <= -144 ? null : d; }
  function paint() {
    Object.keys(rows).forEach(function (ref) {
      var s = byRef[ref];
      if (!s) return;
      var m = Array.isArray(s.meter) && s.meter.length ? s.meter : [0, 0];
      rows[ref].set({ tag: s.kind || "", name: s.name || ref, stereo: m.length > 1, db: s.db <= -100 ? -144 : s.db, muted: !!s.muted,
        l: s.muted ? null : lvl(m[0]), r: s.muted ? null : lvl(m.length > 1 ? m[1] : m[0]) });
    });
  }
  function poll() {
    K.resource("res:meters?ids=" + encodeURIComponent(ids().join(",")))
      .then(function (blob) { return blob.text(); })
      .then(function (t) { var j = JSON.parse(t); live = Date.now(); update((j.strips || []).map(function (s) { return Object.assign({ ref: s.id }, s); })); })
      .catch(function () { if (!live || Date.now() - live > 3000) update(fromState()); })
      .then(function () { setTimeout(poll, K.bool("meters", true) ? 80 : 500); });
  }
  K.render = function () {
    if (!live || Date.now() - live > 3000) update(fromState()); else update(strips);
    if (!started) { started = true; poll(); }
  };
  K.onSize = function () {
    if (!strips.length) return;
    build(strips);
    Object.keys(rows).forEach(function (ref) { rows[ref].fit(); });
    paint();
  };
  K.start();
})();
