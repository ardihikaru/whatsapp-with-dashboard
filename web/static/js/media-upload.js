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
// Image/PDF affordances (all progressive enhancement; the server stays the
// authority): the file picker's accept list follows the Kind select (from the
// option's data-accept, which Go derives from its own sniff allowlist), a
// file that does not fit the Kind either switches the Kind (when exactly one
// kind fits, or it is a PDF) or blocks with a message, an image shows a
// thumbnail and anything else a type badge, a file can be dropped on the
// dropzone or removed again, caption / voice-note fields show only for the
// kinds they apply to, and the form is cleared after a successful send.
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
  var preview = document.getElementById("media-preview");
  var previewImg = document.getElementById("media-preview-img");
  var previewBadge = document.getElementById("media-preview-badge");
  var clearButton = document.getElementById("media-clear");
  var dropHint = document.getElementById("media-drop-hint");
  var kindNotice = document.getElementById("media-kind-notice");
  var captionField = form ? form.querySelector("[data-media-caption]") : null;
  var pttField = form ? form.querySelector("[data-media-ptt]") : null;
  var objectUrl = null;

  function selectedKind() {
    return kindSelect ? kindSelect.value : "";
  }

  // option.data-accept for a kind, as an array ([] = any file).
  function acceptFor(kind) {
    if (!kindSelect) return [];
    for (var i = 0; i < kindSelect.options.length; i++) {
      if (kindSelect.options[i].value === kind) {
        return (kindSelect.options[i].getAttribute("data-accept") || "")
          .split(",").map(function (t) { return t.trim(); }).filter(Boolean);
      }
    }
    return [];
  }

  function isPdf(file) {
    return file.type === "application/pdf" || /\.pdf$/i.test(file.name);
  }

  // Does this file fit the kind? An empty list (document) fits anything; a
  // file with no browser-reported type is not judged here — the server
  // sniffs the bytes and answers either way.
  function fits(file, kind) {
    var list = acceptFor(kind);
    if (!list.length || !file.type) return true;
    return list.indexOf(file.type) !== -1;
  }

  // The one kind to switch to, or "" when it is ambiguous / nothing fits.
  // PDFs are documents; otherwise exactly one non-document kind must list
  // the file's type.
  function kindForFile(file) {
    if (isPdf(file)) return "document";
    if (!file.type || !kindSelect) return "";
    var found = [];
    for (var i = 0; i < kindSelect.options.length; i++) {
      var k = kindSelect.options[i].value;
      if (k !== "document" && acceptFor(k).indexOf(file.type) !== -1) found.push(k);
    }
    return found.length === 1 ? found[0] : "";
  }

  function kindLabel(kind) {
    var opt = kindSelect && kindSelect.querySelector('option[value="' + kind + '"]');
    return opt ? opt.textContent : kind;
  }

  function setNotice(msg) {
    if (!kindNotice) return;
    kindNotice.hidden = !msg;
    kindNotice.textContent = msg || "";
  }

  function revokePreview() {
    if (objectUrl) {
      URL.revokeObjectURL(objectUrl);
      objectUrl = null;
    }
  }

  function renderPreview(file) {
    if (!preview) return;
    revokePreview();
    if (!file) {
      preview.hidden = true;
      if (previewImg) { previewImg.hidden = true; previewImg.removeAttribute("src"); }
      if (previewBadge) previewBadge.hidden = true;
      if (dropHint) dropHint.hidden = false;
      return;
    }
    preview.hidden = false;
    if (dropHint) dropHint.hidden = true;
    var isImage = /^image\/(jpeg|png|webp|gif)$/.test(file.type);
    if (previewImg) {
      previewImg.hidden = !isImage;
      if (isImage) {
        objectUrl = URL.createObjectURL(file);
        previewImg.src = objectUrl;
      } else {
        previewImg.removeAttribute("src");
      }
    }
    if (previewBadge) {
      previewBadge.hidden = isImage;
      if (!isImage) {
        var ext = (file.name.match(/\.([A-Za-z0-9]{1,5})$/) || [])[1];
        previewBadge.textContent = isPdf(file) ? "PDF" : (ext ? ext.toUpperCase() : "FILE");
        previewBadge.setAttribute("data-file-type", isPdf(file) ? "pdf" : "other");
      }
    }
  }

  // Caption applies to image/video/document, voice note to audio only. A
  // hidden field is also disabled so a stale value is never submitted.
  function syncFieldsForKind() {
    var kind = selectedKind();
    if (captionField) {
      var showCaption = kind !== "audio";
      captionField.hidden = !showCaption;
      var ta = captionField.querySelector("textarea");
      if (ta) ta.disabled = !showCaption;
    }
    if (pttField) {
      var showPtt = kind === "audio";
      pttField.hidden = !showPtt;
      var cb = pttField.querySelector("input");
      if (cb) {
        cb.disabled = !showPtt;
        if (!showPtt) cb.checked = false;
      }
    }
  }

  function resetProgress() {
    var wrap = document.getElementById("media-progress");
    var bar = document.getElementById("media-progress-bar");
    if (wrap) wrap.hidden = true;
    if (bar) bar.style.width = "0%";
  }

  function clearFile() {
    fileInput.value = "";
    setNotice("");
    checkFile();
  }

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
    renderPreview(file);
    if (!file) {
      if (chip) chip.hidden = true;
      return;
    }
    if (chip) {
      chip.hidden = false;
      chip.textContent = file.name + " (" + formatBytes(file.size) + ")";
    }
    if (!fits(file, selectedKind())) {
      var hint = kindForFile(file);
      showError(
        "This file (" + (file.type || "unknown type") + ") cannot be sent as " +
        kindLabel(selectedKind()) + "." +
        (hint ? " Switch Kind to " + kindLabel(hint) + "." : " Pick a Kind that fits it, or Document.")
      );
      return;
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

  // A newly chosen file that does not fit the current Kind switches the
  // Kind when that is unambiguous (and says so); otherwise checkFile blocks
  // with a message. Switching on KIND change is deliberately NOT done: the
  // operator just chose that kind, so they get the warning, not a reversal.
  function onFileChosen() {
    var file = fileInput.files && fileInput.files[0];
    setNotice("");
    if (file && kindSelect && !fits(file, selectedKind())) {
      var target = kindForFile(file);
      if (target && target !== selectedKind()) {
        kindSelect.value = target;
        setNotice("Kind switched to " + kindLabel(target) + " to fit this file.");
        syncKind();
        return;
      }
    }
    checkFile();
  }
  fileInput.addEventListener("change", onFileChosen);
  if (clearButton) clearButton.addEventListener("click", clearFile);

  // Drag and drop onto the dropzone. Assigning DataTransfer.files to the
  // input keeps the file in the real multipart submit; one file only.
  ["dragenter", "dragover"].forEach(function (name) {
    dropzone.addEventListener(name, function (evt) {
      evt.preventDefault();
      dropzone.classList.add("is-dragover");
    });
  });
  ["dragleave", "dragend"].forEach(function (name) {
    dropzone.addEventListener(name, function () {
      dropzone.classList.remove("is-dragover");
    });
  });
  dropzone.addEventListener("drop", function (evt) {
    evt.preventDefault();
    dropzone.classList.remove("is-dragover");
    var files = evt.dataTransfer && evt.dataTransfer.files;
    if (!files || !files.length) return;
    try {
      var dt = new DataTransfer();
      dt.items.add(files[0]);
      fileInput.files = dt.files;
    } catch (e) {
      return;
    }
    onFileChosen();
  });

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
      var activeKind = opt ? opt.value : "";
      limitEls.forEach(function (el) {
        el.classList.toggle("is-active", el.getAttribute("data-media-limit") === activeKind);
      });
      var accept = opt ? opt.getAttribute("data-accept") : "";
      if (accept) fileInput.setAttribute("accept", accept);
      else fileInput.removeAttribute("accept");
    }
    syncFieldsForKind();
    checkFile();
  }
  if (kindSelect) kindSelect.addEventListener("change", function () {
    setNotice("");
    syncKind();
  });
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

  // A failed or aborted request never reaches 100%, so without this the bar
  // would stay frozen mid-way until the next send.
  if (form) {
    ["htmx:responseError", "htmx:sendError", "htmx:sendAbort", "htmx:timeout", "htmx:xhr:abort"].forEach(function (name) {
      form.addEventListener(name, resetProgress);
    });

    // POST /send/media answers 200 for BOTH outcomes (the failure is an
    // alert-error partial), so success is read from the response body, not
    // the status. After a success the file, caption and voice-note are
    // cleared so the same attachment is not re-sent by a second click;
    // recipient, routing and kind are kept for sending several in a row.
    form.addEventListener("htmx:afterRequest", function (evt) {
      var xhr = evt.detail && evt.detail.xhr;
      if (!xhr || evt.detail.elt !== form) return;
      resetProgress();
      if (evt.detail.successful && /alert-success/.test(xhr.responseText || "")) {
        clearFile();
        var ta = captionField && captionField.querySelector("textarea");
        if (ta) ta.value = "";
        var cb = pttField && pttField.querySelector("input");
        if (cb) cb.checked = false;
      }
    });
  }
})();
