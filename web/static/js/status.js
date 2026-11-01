// /status page — colour swatches, live text preview, the recipients
// checklist (search/select-all/live count) and the confirm-before-posting
// step. Deliberately its own file, NOT a reuse of media-upload.js (which
// binds hard-coded element ids this page does not have) — plain IIFE over
// the DOM, same house pattern as charts-init.js/nav-drawer.js.
//
// Everything here is progressive enhancement: status.html's forms submit
// and validate fine with this script absent (the server enforces
// non-empty recipients regardless) — this only adds the colour picker's
// live preview, the checklist's search/count, and an extra confirm step
// before a real POST goes out.
(function () {
  "use strict";

  var forms = document.querySelectorAll("[data-status-form]");
  if (!forms.length) return;

  // ---------- colour swatches + live text preview --------------------------
  forms.forEach(function (form) {
    var swatchWrap = form.querySelector("[data-status-swatches]");
    var bgInput = form.querySelector("[data-status-background-input]");
    var preview = form.querySelector("[data-status-text-preview]");
    var previewText = form.querySelector("[data-status-text-preview-text]");
    var textInput = form.querySelector("[data-status-text-input]");

    if (swatchWrap && bgInput) {
      var swatches = swatchWrap.querySelectorAll("[data-status-swatch]");
      swatches.forEach(function (sw) {
        sw.addEventListener("click", function () {
          swatches.forEach(function (other) {
            other.classList.remove("is-active");
            other.setAttribute("aria-pressed", "false");
          });
          sw.classList.add("is-active");
          sw.setAttribute("aria-pressed", "true");
          bgInput.value = sw.getAttribute("data-argb") || "0";
          if (preview) {
            // The one background-color transition this page has; see
            // status.css's header comment on why it is exempt from the
            // transform/opacity-only rule everywhere else.
            preview.style.backgroundColor = sw.style.backgroundColor;
          }
        });
      });
    }

    if (textInput && previewText) {
      var syncPreview = function () {
        previewText.textContent = textInput.value.trim() || "What's on your mind?";
      };
      textInput.addEventListener("input", syncPreview);
      syncPreview();
    }
  });

  // ---------- recipients: search, select all/none, live count -------------
  forms.forEach(function (form) {
    var wrap = form.querySelector("[data-status-recipients]");
    if (!wrap) return;
    var search = wrap.querySelector("[data-status-recipients-search]");
    var rows = wrap.querySelectorAll("[data-status-recipients-row]");
    var boxes = wrap.querySelectorAll("[data-status-recipient]");
    var countEl = wrap.querySelector("[data-status-recipients-count]");
    var selectAll = wrap.querySelector("[data-status-select-all]");
    var selectNone = wrap.querySelector("[data-status-select-none]");

    // Instant — no transition — recipient count is the consent surface
    // (status.css's header comment); it must never lag a click.
    function updateCount() {
      if (!countEl) return;
      var n = 0;
      boxes.forEach(function (b) { if (b.checked) n++; });
      countEl.textContent = n + " selected";
      form.setAttribute("data-status-recipient-count", String(n));
    }

    boxes.forEach(function (b) { b.addEventListener("change", updateCount); });
    updateCount();

    if (search) {
      search.addEventListener("input", function () {
        var q = search.value.trim().toLowerCase();
        rows.forEach(function (row) {
          var name = (row.getAttribute("data-name") || "").toLowerCase();
          var phone = (row.getAttribute("data-phone") || "").toLowerCase();
          var match = !q || name.indexOf(q) !== -1 || phone.indexOf(q) !== -1;
          if (match) row.removeAttribute("hidden");
          else row.setAttribute("hidden", "");
        });
      });
    }
    if (selectAll) {
      selectAll.addEventListener("click", function () {
        boxes.forEach(function (b) { if (!b.closest("[hidden]")) b.checked = true; });
        updateCount();
      });
    }
    if (selectNone) {
      selectNone.addEventListener("click", function () {
        boxes.forEach(function (b) { b.checked = false; });
        updateCount();
      });
    }
  });

  // ---------- confirm-before-posting step -----------------------------------
  // Each form's real submit button is intercepted exactly once: the first
  // click shows the confirm panel (naming the live recipient count) instead
  // of submitting; the panel's own "Yes, post" button sets a per-form flag
  // and re-submits, which this listener then lets through. Cancel hides the
  // panel with no other side effect. htmx's own submit interception runs on
  // the SAME "submit" event, so preventDefault here stops it too.
  forms.forEach(function (form) {
    var confirmPanel = form.querySelector("[data-status-confirm]");
    var confirmCount = form.querySelector("[data-status-confirm-count]");
    var confirmGo = form.querySelector("[data-status-confirm-go]");
    var confirmCancel = form.querySelector("[data-status-confirm-cancel]");
    if (!confirmPanel || !confirmGo) return;

    var confirmed = false;

    form.addEventListener("submit", function (ev) {
      if (confirmed) {
        confirmed = false; // one confirmation covers exactly one submit
        return;
      }
      ev.preventDefault();
      if (typeof ev.stopImmediatePropagation === "function") ev.stopImmediatePropagation();
      var n = form.getAttribute("data-status-recipient-count") || "0";
      if (confirmCount) confirmCount.textContent = n;
      confirmPanel.removeAttribute("hidden");
    }, true); // capture: run before htmx's own submit listener

    confirmGo.addEventListener("click", function () {
      confirmed = true;
      confirmPanel.setAttribute("hidden", "");
      if (typeof form.requestSubmit === "function") form.requestSubmit();
      else form.submit();
    });

    if (confirmCancel) {
      confirmCancel.addEventListener("click", function () {
        confirmPanel.setAttribute("hidden", "");
      });
    }
  });
})();
