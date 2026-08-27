// Media-attachment form helpers for /send's Media tab: selected-filename
// display, a client-side size pre-check, and an upload progress bar.
//
// This is a PRE-CHECK only. The real limit is enforced server-side by
// http.MaxBytesReader in parseMediaSendRequest (internal/handler/
// api_media.go) — this script exists purely so an operator on a slow
// connection finds out a file is too big before spending minutes uploading
// it, not to replace that check. A form with JS disabled still submits and
// still gets the server's answer.
//
// No library. Plain IIFE, following nav-drawer.js's shape: early return
// when its elements are absent, so the no-JS path stays the default.
(function () {
  "use strict";

  var dropzone = document.querySelector("[data-media-dropzone]");
  var fileInput = document.getElementById("media-file-input");
  if (!dropzone || !fileInput) return;

  var chip = document.getElementById("media-file-chip");
  var sizeError = document.getElementById("media-size-error");
  var maxBytes = parseInt(dropzone.getAttribute("data-max-bytes"), 10) || 0;
  var submitButton = fileInput.form ? fileInput.form.querySelector("button[type=submit]") : null;

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
    // Disabled, not just warned: an operator who ignores the chip text
    // would otherwise wait through a doomed upload only to hit the
    // server's 413 at the end.
    if (submitButton) submitButton.disabled = true;
  }

  fileInput.addEventListener("change", function () {
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
    if (maxBytes > 0 && file.size > maxBytes) {
      showError(
        "This file (" + formatBytes(file.size) + ") is larger than the " +
        formatBytes(maxBytes) + " limit configured on this deployment."
      );
    }
  });

  // Progress bar, driven by htmx's own htmx:xhr:progress event. htmx fires
  // this for BOTH the upload phase (xhr.upload) and the (here, tiny)
  // response-download phase, under the same event name with no field to
  // tell them apart — only detail.elt (the element that triggered the
  // request, set by htmx's own dispatch helper) identifies which form this
  // tick belongs to. Good enough for a progress bar: the download phase for
  // a small HTML fragment response is over in a tick or two either way.
  var progressWrap = document.getElementById("media-progress");
  var progressBar = document.getElementById("media-progress-bar");
  if (progressWrap && progressBar && fileInput.form) {
    fileInput.form.addEventListener("htmx:xhr:progress", function (evt) {
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
