// Stream: go live / stop (pressed and held, the widget's holdSeconds),
// recording, how long it's been live, and bitrate, dropped frames and CPU
// with a plain-words health line ("your upload is struggling: …").
(function () {
  "use strict";
  var K = window.Kit, JK = window.JK, main = document.getElementById("main"), hdr = null, clockEl = null;

  function since(iso) { return iso ? K.hms(Date.now() - Date.parse(iso)) : "00:00:00"; }
  function num(label, value, tone) {
    var d = K.el("div", "num");
    d.appendChild(K.el("span", "l", label));
    d.appendChild(K.el("span", "v" + (tone ? " " + tone : ""), value));
    return d;
  }
  function rate(k) { return k >= 1000 ? (k / 1000).toFixed(1) + " Mbps" : (k || 0) + " kbps"; }

  K.render = function (st) {
    if (!hdr) hdr = JK.header({ title: "Stream" });
    if (st.connected === false || st.error) {
      hdr.set({ status: "bad", source: st.source || "OBS", right: "" });
      clockEl = null;
      return K.empty(main, st.error || "OBS isn't answering");
    }
    var on = !!st.streamOn, hold = st.hold || 5, ro = !!st.readOnly;
    hdr.set({ status: on ? (st.health === "bad" ? "bad" : "live") : "ok", source: st.source || "OBS", right: on ? "ON AIR" : "OFF AIR" });
    main.textContent = "";
    var s = K.el("div", on ? "on" : ""); s.id = "s";
    var top = K.el("div", "top");
    top.appendChild(K.el("span", "jk-dot" + (on ? " live" : "")));
    clockEl = K.el("span", "clock", on ? since(st.streamSince) : "--:--:--");
    top.appendChild(clockEl);
    top.appendChild(K.el("span", "what", st.streamReconnecting ? "RECONNECTING" : on ? (st.recordOn ? "LIVE · RECORDING" : "LIVE") : (st.recordOn ? "RECORDING" : "")));
    s.appendChild(top);

    var showRec = K.bool("record", true), btns = K.el("div", "btns" + (showRec ? "" : " one"));
    var go = K.holdButton(on ? "Stop stream" : "Go live", on ? "LIVE" : "OFF AIR", { seconds: hold, tone: on ? "pgm" : "pvw", verb: on ? "STOPS" : "STARTS",
      action: "stream", params: { on: !on, confirmed: true } });
    if (ro) go.set({ disabled: true });
    btns.appendChild(go);
    if (showRec) {
      var rec = K.holdButton(st.recordOn ? "Stop rec" : "Record", "", { seconds: hold, tone: "pgm", verb: st.recordOn ? "STOPS" : "STARTS",
        action: "record", params: { on: !st.recordOn, confirmed: true } });
      if (ro) rec.set({ disabled: true });
      btns.appendChild(rec);
    }
    s.appendChild(btns);

    var room = main.clientHeight;
    if (K.bool("stats", true) && room >= 150) {
      var nums = K.el("div", "nums"), d = st.dropped || 0, cpu = st.cpu || 0;
      nums.appendChild(num("Bitrate", on ? rate(st.kbps) : "–"));
      nums.appendChild(num("Dropped", on ? d.toFixed(1) + " %" : "–", on ? (d >= 5 ? "bad" : d >= 1 ? "warn" : "good") : ""));
      nums.appendChild(num("CPU", Math.round(cpu) + " %", cpu >= 90 ? "bad" : cpu >= 70 ? "warn" : ""));
      s.appendChild(nums);
    }
    if (room >= 190 && st.healthText && st.health !== "ok" && st.health !== "off") s.appendChild(K.el("div", "health " + (st.health || ""), st.healthText));
    main.appendChild(s);
  };
  setInterval(function () { if (clockEl && K.state.streamOn) clockEl.textContent = since(K.state.streamSince); }, 1000);
  K.start();
})();
