// /status/schedules form (status_schedule_form.html): keeps the one-line
// summary ("Mon, Wed · once each day, at a random time between 07:00 and
// 10:27 Asia/Jakarta") in step with the weekday chips, the window inputs and
// the timezone as they change. Progressive enhancement only — the server
// renders the same sentence (handler.statusScheduleSummary) for the initial
// paint and for a browser with JS disabled, and validates everything itself.
// The sentence is rewritten WITHOUT animation on purpose: it updates on every
// keystroke. The colour swatches and the recipients checklist are driven by
// status.js through the same data-status-* contract /status uses.
//
// No library. Plain IIFE, same shape as nav-drawer.js.
(function () {
  "use strict";

  var form = document.querySelector("[data-ss-form]");
  if (!form) return;
  var out = form.querySelector("[data-ss-summary]");
  var start = form.querySelector("[data-ss-start]");
  var end = form.querySelector("[data-ss-end]");
  var tz = form.querySelector("[data-ss-tz]");
  if (!out || !start || !end || !tz) return;

  // The chips are laid out Sunday-first, but the sentence lists days
  // Monday..Sunday (ISO order), exactly as the server-rendered copy does.
  var ISO = [["1", "Mon"], ["2", "Tue"], ["3", "Wed"], ["4", "Thu"], ["5", "Fri"], ["6", "Sat"], ["7", "Sun"]];

  function render() {
    var on = {};
    form.querySelectorAll("[data-ss-weekday]").forEach(function (box) {
      if (box.checked) on[box.value] = true;
    });
    var names = ISO.filter(function (d) { return on[d[0]]; }).map(function (d) { return d[1]; });
    if (!names.length) {
      out.textContent = "Pick at least one weekday.";
      return;
    }
    out.textContent = names.join(", ") + " · once each day, at a random time between " +
      start.value + " and " + end.value + " " + tz.value;
  }

  form.addEventListener("change", render);
  form.addEventListener("input", render);
})();
