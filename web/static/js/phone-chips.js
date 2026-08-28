// Chip editor for /verify's "Phone numbers" field.
//
// PROGRESSIVE ENHANCEMENT, exactly like media-upload.js and nav-drawer.js:
// the server contract is a plain `<textarea name="phones">`, one number per
// line, and a browser with JS disabled submits that textarea completely
// unmodified and gets a full answer back (verify.html's plain
// action/method fallback). This script never changes what gets posted; it
// only changes how the operator EDITS what gets posted, and every edit is
// written straight back into the same textarea before it can ever be
// submitted — see syncTextarea below. There is no second code path on the
// server for "the chip editor was used".
//
// No library. Plain IIFE, following nav-drawer.js's shape: early return
// when its elements are absent, so the no-JS path stays the default.
(function () {
  "use strict";

  // Server-side ceiling — internal/handler/contacts_check.go's
  // maxVerifyPhones. Duplicated here as a UI-only convenience (disable the
  // submit button before wasting a round trip); the server re-checks this
  // independently and is the only enforcement that actually matters.
  var MAX_PHONES = 100;

  var container = document.querySelector("[data-phone-chips]");
  if (!container) return;
  var textarea = container.querySelector("textarea[name='phones']");
  if (!textarea) return;
  var form = textarea.form;
  var submitButton = form ? form.querySelector("button[type=submit]") : null;

  // Only now does the chip UI apply — CSS keys off this the same way
  // responsive.css keys off nav-drawer.js's data-nav-js.
  container.setAttribute("data-chip-js", "");

  var wrap = document.createElement("div");
  wrap.className = "chip-input-wrap";

  var list = document.createElement("ul");
  list.className = "chip-list";
  list.setAttribute("role", "list");

  var input = document.createElement("input");
  input.type = "tel";
  input.className = "chip-input";
  input.setAttribute("aria-labelledby", textarea.getAttribute("aria-labelledby") || "");
  input.setAttribute(
    "aria-describedby",
    (textarea.getAttribute("aria-labelledby") || "") + " verify-phone-counter"
  );
  input.placeholder = "Type a number, press Enter";
  input.autocomplete = "off";

  wrap.appendChild(list);
  wrap.appendChild(input);

  var counter = document.createElement("p");
  counter.className = "chip-counter";
  counter.id = "verify-phone-counter";
  counter.setAttribute("aria-live", "polite");

  textarea.insertAdjacentElement("afterend", counter);
  textarea.insertAdjacentElement("afterend", wrap);

  var chips = [];

  // explode splits on newline, comma OR semicolon — a superset of the
  // server's own newline/comma split (PostVerify's splitPhonesInput), so a
  // pasted semicolon-separated export explodes into chips too even though
  // the server would otherwise treat it as one long token.
  function explode(raw) {
    return String(raw)
      .split(/[\n,;]+/)
      .map(function (s) { return s.trim(); })
      .filter(Boolean);
  }

  function syncTextarea() {
    textarea.value = chips.join("\n");
  }

  function syncCounter() {
    var over = chips.length > MAX_PHONES;
    counter.textContent = chips.length + "/" + MAX_PHONES + " numbers";
    counter.classList.toggle("chip-counter-over", over);
    if (submitButton) submitButton.disabled = chips.length === 0 || over;
  }

  function render() {
    list.textContent = "";
    chips.forEach(function (number, idx) {
      var li = document.createElement("li");
      li.className = "chip";

      var text = document.createElement("span");
      text.className = "chip-text";
      text.textContent = number;

      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "chip-remove";
      remove.setAttribute("aria-label", "Remove " + number);
      remove.textContent = "×";
      remove.addEventListener("click", function () {
        chips.splice(idx, 1);
        render();
        input.focus();
      });

      li.appendChild(text);
      li.appendChild(remove);
      list.appendChild(li);
    });
    syncTextarea();
    syncCounter();
  }

  // flashDuplicate rejects a re-typed number with a brief visual shake on
  // the EXISTING chip, rather than silently dropping the input or adding a
  // second copy of it.
  function flashDuplicate(idx) {
    var li = list.children[idx];
    if (!li) return;
    li.classList.remove("chip-duplicate");
    void li.offsetWidth; // restart the animation if it is still running
    li.classList.add("chip-duplicate");
    li.addEventListener(
      "animationend",
      function handler() {
        li.classList.remove("chip-duplicate");
        li.removeEventListener("animationend", handler);
      },
      { once: true }
    );
  }

  // addChip returns false (and flashes the existing chip) for a duplicate
  // — a plain case-insensitive string match on the typed value, not a full
  // wa.NormalizePhone-equivalent parse, which is a server-side concern and
  // stays one; this is only meant to catch the common case of the same
  // text pasted or typed twice.
  function addChip(raw) {
    var value = raw.trim();
    if (!value) return true;
    var dupIndex = chips.findIndex(function (c) {
      return c.toLowerCase() === value.toLowerCase();
    });
    if (dupIndex !== -1) {
      flashDuplicate(dupIndex);
      return false;
    }
    chips.push(value);
    return true;
  }

  input.addEventListener("keydown", function (e) {
    if (e.key === "Enter" || e.key === ",") {
      e.preventDefault();
      if (input.value.trim() === "") return;
      addChip(input.value);
      input.value = "";
      render();
      return;
    }
    if (e.key === "Backspace" && input.value === "" && chips.length) {
      chips.pop();
      render();
    }
  });

  input.addEventListener("paste", function (e) {
    var clipboard = e.clipboardData || window.clipboardData;
    var text = clipboard ? clipboard.getData("text") : "";
    if (!text || !/[\n,;]/.test(text)) return; // a single value: let it land in the input normally
    e.preventDefault();
    explode(text).forEach(addChip);
    input.value = "";
    render();
  });

  // A number left typed but not yet committed must not vanish silently if
  // the operator tabs away.
  input.addEventListener("blur", function () {
    if (input.value.trim() === "") return;
    addChip(input.value);
    input.value = "";
    render();
  });

  // Seed from whatever the textarea already held (e.g. a value set before
  // this script ran) so enabling JS never loses content already typed.
  explode(textarea.value).forEach(addChip);
  render();
})();
