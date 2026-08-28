// Media-attachment form helpers for /send's Media tab: selected-filename
// display, a client-side per-kind size pre-check, an active-limit highlight
// that follows the "kind" select, and an upload progress bar.
//
// This is a PRE-CHECK only. The real limit is enforced server-side, per
// kind, by parseMediaSendRequest's size check (internal/handler/
// api_media.go's mediaKindLimit) — this script exists purely so an operator
// on a slow connection finds out a file is too big before spending minutes
// uploading it, not to replace that check. A form with JS disabled still
// submits and still gets the server's answer, with the same kind/size/limit
// wording either way (send_result.html renders the same message this
// pre-check only anticipates).
//
// The pre-check is a HARD BLOCK, not a warning an operator can ignore: an
// oversize file both disables the submit button AND (belt-and-suspenders,
// in case some browser's implicit-submit-on-Enter ever reaches a disabled
// button) is caught again on the form's own "submit" event. Uploading a
// file the server is guaranteed to reject wastes the operator's upload time
// for nothing, so there is no "warn but still allow" middle ground here.
//
// No library. Plain IIFE, following nav-drawer.js's shape: early return
// when its elements are absent, so the no-JS path stays the default.
(function () {
  "use strict";

  var dropzone = document.querySelector("[data-media-dropzone]");
  var fileInput = document.getElementById("media-file-input");
  if (!dropzone || !fileInput) return;

  var form = fileInput.form;
  var chip = document.getElementById("media-file-chip");
  var sizeError = document.getElementById("media-size-error");
  var kindSelect = form ? form.querySelector('select[name="kind"]') : null;
  var limitEls = document.querySelectorAll("[data-media-limit]");
  var submitButton = form ? form.querySelector("button[type=submit]") : null;

  function currentMaxBytes() {
    return parseInt(dropzone.getAttribute("data-max-bytes"), 10) || 0;
  }

  function formatBytes(n) {
    if (n >= 1024 * 1024) return (n / (1024 * 1024)).toFixed(1) + " MB";
    if (n >= 1024) return (n / 1024).toFixed(1) + " KB";
    return n + " B";
  }

  function clearError() {
    if (sizeError) {
      sizeError.hidden = true;
      sizeError.textContent = "";
    }
    if (submitButton) submitButton.disabled = false;
  }

  function showError(msg) {
    if (sizeError) {
      sizeError.hidden = false;
      sizeError.textContent = msg;
    }
    // Disabled, not just warned — see this file's header comment on why
    // there is no "warn but still allow" middle ground here.
    if (submitButton) submitButton.disabled = true;
  }

  // Re-validates whatever file is currently chosen against currentMaxBytes()
  // — called both when the file changes AND when the kind (and therefore
  // the limit) changes, so switching "Video" to "Image" after picking an
  // 8 MB file immediately (re-)flags it rather than waiting for a doomed
  // submit.
  function checkFile() {
    var file = fileInput.files && fileInput.files[0];
    clearError();
    if (!file) {
      if (chip) chip.hidden = true;
      return;
    }
    if (chip) {
      chip.hidden = false;
      chip.textContent = file.name + " (" + formatBytes(file.size) + ")";
    }
    var maxBytes = currentMaxBytes();
    if (maxBytes > 0 && file.size > maxBytes) {
      showError(
        "This file (" + formatBytes(file.size) + ") is larger than the " +
        formatBytes(maxBytes) + " limit for this kind. Choose a smaller " +
        "file, or pick a different kind if one fits."
      );
    }
  }

  fileInput.addEventListener("change", checkFile);

  // Reflect the selected kind: the dropzone's data-max-bytes tracks
  // whichever <option> is selected (each carries its own per-kind
  // data-max-bytes — see send.html), and the matching entry in the
  // "Image 5 MB · Video 16 MB · …" summary line is highlighted, so the
  // operator can see which number the pre-check is using without opening
  // dev tools.
  function syncKind() {
    if (kindSelect) {
      var opt = kindSelect.options[kindSelect.selectedIndex];
      var maxBytes = opt ? opt.getAttribute("data-max-bytes") : null;
      if (maxBytes) dropzone.setAttribute("data-max-bytes", maxBytes);
      var selectedKind = opt ? opt.value : "";
      limitEls.forEach(function (el) {
        el.classList.toggle("is-active", el.getAttribute("data-media-limit") === selectedKind);
      });
    }
    checkFile();
  }
  if (kindSelect) kindSelect.addEventListener("change", syncKind);
  syncKind(); // initial sync on load — matches the select's default (first) option

  // Belt-and-suspenders: block a real submit outright if the size error is
  // showing, even though the disabled submit button (showError, above)
  // should already have made this unreachable through the button itself.
  if (form) {
    form.addEventListener("submit", function (evt) {
      if (sizeError && !sizeError.hidden) evt.preventDefault();
    });
  }

  // Progress bar, driven by htmx's own htmx:xhr:progress event. htmx fires
  // this for BOTH the upload phase (xhr.upload) and the (here, tiny)
  // response-download phase, under the same event name with no field to
  // tell them apart — only detail.elt (the element that triggered the
  // request, set by htmx's own dispatch helper) identifies which form this
  // tick belongs to. Good enough for a progress bar: the download phase for
  // a small HTML fragment response is over in a tick or two either way.
  var progressWrap = document.getElementById("media-progress");
  var progressBar = document.getElementById("media-progress-bar");
  if (progressWrap && progressBar && form) {
    form.addEventListener("htmx:xhr:progress", function (evt) {
      if (!evt.detail || !evt.detail.lengthComputable || !evt.detail.total) return;
      var pct = Math.round((evt.detail.loaded / evt.detail.total) * 100);
      progressWrap.hidden = false;
      progressBar.style.width = pct + "%";
      if (pct >= 100) {
        // Left visible briefly rather than hidden instantly: an
        // htmx:afterRequest swap of #send-result follows almost
        // immediately and the panel scrolls away with it.
        setTimeout(function () {
          progressWrap.hidden = true;
          progressBar.style.width = "0%";
        }, 400);
      }
    });
  }
})();
