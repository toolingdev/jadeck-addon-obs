// Program: a live picture of what's on air in OBS (OBS's own render of
// the program scene, fetched as the add-on's "program.jpg" resource 1-2
// times a second), with LIVE and REC badges and the scene's name.
(function () {
  "use strict";
  var K = window.Kit, JK = window.JK, main = document.getElementById("main"), hdr = null;
  var box = null, img = null, badges = null, wait = null, url = "", timer = 0, busy = false, lastOk = 0;

  function build() {
    if (box) return;
    main.textContent = "";
    box = K.el("div"); box.id = "pic";
    img = document.createElement("img"); img.alt = "";
    img.onerror = function () { img.style.visibility = "hidden"; };
    img.onload = function () { img.style.visibility = ""; };
    badges = K.el("div", "badges");
    wait = K.el("div", "wait", "");
    box.appendChild(img); box.appendChild(badges); box.appendChild(wait);
    main.appendChild(box);
  }
  function since(iso) { return iso ? K.hms(Date.now() - Date.parse(iso)) : ""; }
  function drawBadges(st) {
    badges.textContent = "";
    if (st.streamOn) badges.appendChild(K.el("span", "b live", "● LIVE " + since(st.streamSince)));
    if (st.recordOn) badges.appendChild(K.el("span", "b rec", "● REC"));
  }
  function fetchPic() {
    clearTimeout(timer);
    var fps = parseFloat(K.settings.fps) || 1, every = Math.max(400, 1000 / Math.min(2, Math.max(0.5, fps)));
    timer = setTimeout(fetchPic, every);
    if (busy || K.state.connected === false || document.hidden) return;
    busy = true;
    var w = Math.min(1280, Math.max(320, Math.round(main.clientWidth * (window.devicePixelRatio || 1) / 160) * 160));
    K.resource("res:program.jpg?w=" + w).then(function (blob) {
      var u = URL.createObjectURL(blob), old = url;
      url = u; img.src = u; lastOk = Date.now(); wait.textContent = "";
      if (old) setTimeout(function () { URL.revokeObjectURL(old); }, 1000);
    }).catch(function (e) {
      if (!lastOk || Date.now() - lastOk > 4000) { img.removeAttribute("src"); wait.textContent = K.editing ? "The picture from OBS shows here" : "No picture: " + e.message; }
    }).then(function () { busy = false; });
  }
  K.render = function (st) {
    if (!hdr) hdr = JK.header({ title: "Program" });
    if (st.connected === false || st.error) {
      hdr.set({ status: "bad", source: st.source || "OBS", right: "" });
      box = null;
      return K.empty(main, st.error || "OBS isn't answering");
    }
    hdr.set({ status: st.streamOn ? "live" : "ok", source: st.source || "OBS", right: st.program || "" });
    build();
    drawBadges(st);
    if (!timer) fetchPic();
  };
  setInterval(function () { if (badges && K.state.streamOn) drawBadges(K.state); }, 1000);
  K.start();
})();
