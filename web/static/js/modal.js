// Native <dialog> popups, triggered by [data-modal-open]/[data-modal-close]
// buttons (/outgoing-chat's "Ringkasan per Sesi" and "Jarak rata-rata"
// sections). No library — mirrors nav-drawer.js: a plain IIFE over the DOM,
// loaded sitewide since a dialog trigger with no matching element is cheap
// and harmless on a page that has none (same reasoning as self-convo.js).
//
// A dialog that contains an ECharts canvas starts life closed — hidden by
// the UA stylesheet until .showModal() runs — so charts-init.js's own
// DOMContentLoaded pass initialises it against a 0x0 container. Opening the
// dialog here dispatches 'wsm:charts-refresh' (see charts-init.js) so any
// chart inside gets a fresh init once the container has real dimensions.
(function () {
  "use strict";

  document.addEventListener("click", function (e) {
    var openBtn = e.target.closest("[data-modal-open]");
    if (openBtn) {
      var dialog = document.getElementById(openBtn.getAttribute("data-modal-open"));
      if (!dialog || typeof dialog.showModal !== "function") return;
      dialog.showModal();
      // Wait one frame so the dialog's own layout has landed before any
      // chart inside it measures its container.
      window.requestAnimationFrame(function () {
        document.dispatchEvent(new CustomEvent("wsm:charts-refresh"));
      });
      return;
    }

    var closeBtn = e.target.closest("[data-modal-close]");
    if (closeBtn) {
      var owner = closeBtn.closest("dialog");
      if (owner) owner.close();
      return;
    }

    // Click-outside-to-dismiss: a click landing on the <dialog> element
    // itself (not any of its content — content clicks never bubble to a
    // target of the dialog, since the dialog's padding box IS the backdrop
    // area) closes it.
    if (e.target.tagName === "DIALOG" && e.target.hasAttribute("open")) {
      e.target.close();
    }
  });
})();
