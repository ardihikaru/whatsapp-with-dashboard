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

// Convo schedule form (schedule_form.html): the recurrence radios pick
// daily/weekly/every-N-days, but only weekly needs the Mon..Sun checkbox
// row and only "every N days" needs the interval number field. Both
// sections stay in the DOM and POST their values regardless — this is
// PURELY a display convenience, not validation: validateScheduleForm
// (self_convo_schedule_ui.go) only requires whichever section matches the
// picked recurrence, so a browser with JS disabled sees both sections at
// once (mildly redundant, never wrong) rather than a broken form. No
// library. Plain IIFE, same shape as nav-drawer.js.
(function () {
  "use strict";

  var radios = document.querySelectorAll('input[name="recurrence"]');
  if (!radios.length) return;

  function sync() {
    var picked = document.querySelector('input[name="recurrence"]:checked');
    var value = picked ? picked.value : "";
    var sections = document.querySelectorAll("[data-recurrence-section]");
    for (var i = 0; i < sections.length; i++) {
      sections[i].hidden = sections[i].getAttribute("data-recurrence-section") !== value;
    }
  }

  for (var i = 0; i < radios.length; i++) {
    radios[i].addEventListener("change", sync);
  }
  sync();
})();
