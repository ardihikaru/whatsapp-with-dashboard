// /self-convo's "start at" field: a datetime-local input plus a hidden
// tz_offset carrying the BROWSER's own UTC offset in minutes
// (Date.prototype.getTimezoneOffset — UTC = local + offset), so the server
// (parseStartAt, self_convo_ui.go) interprets the typed time in the
// operator's own timezone instead of assuming UTC.
//
// PROGRESSIVE ENHANCEMENT, same shape as nav-drawer.js/phone-chips.js: a
// browser with JS disabled posts tz_offset's empty default, and
// parseStartAt treats that as 0 (UTC) — documented on the field itself
// (trigger.html's help text) so a no-JS submission is wrong in a labelled,
// not silent, way.
//
// No library. Plain IIFE.
(function () {
  "use strict";

  var offset = String(new Date().getTimezoneOffset());
  var fields = document.querySelectorAll('input[name="tz_offset"]');
  for (var i = 0; i < fields.length; i++) {
    fields[i].value = offset;
  }
})();
