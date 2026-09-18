// Known Groups picker (web/templates/partials/known_group_picker.html):
// searchable <input list="..."> bound to a <datalist>, replacing the old
// flat radio-button list, which did not scale — one production session has
// 370 joined groups, and a page of 370 radio rows with no search is
// unusable. A native datalist gives free type-to-filter with no framework.
//
// A datalist <option> only round-trips its VALUE (the label the operator
// typed/picked), not a separate {name, jid} pair, so the partial also
// renders a <script type="application/json" data-known-group-options> blob
// (same convention as charts-init.js's data-chart blobs) mapping each label
// back to its real name and JID. This script's only job is: on a pick,
// look the typed value up in that map and copy the two fields into the
// surrounding form's group_jid/name inputs — the same effect the old
// per-radio inline onclick had.
//
// PROGRESSIVE ENHANCEMENT floor: with JS disabled the datalist still offers
// free-text typing (a native browser feature), but group_jid/name have to
// be filled in by hand since nothing runs to copy the pick over.
//
// Re-runs on htmx:afterSwap, same shape as charts-init.js: the whole
// #known-group-picker subtree (and its <input>) is replaced every time the
// operator changes the Session select, so there is no stale-listener risk
// to guard against — each swap's new <input> gets its own fresh listener.
(function () {
  "use strict";

  function wire(picker) {
    var input = picker.querySelector("[data-known-group-search]");
    var blob = picker.querySelector("[data-known-group-options]");
    if (!input || !blob) return;

    var options;
    try {
      options = JSON.parse(blob.textContent);
    } catch (e) {
      return;
    }
    var byLabel = {};
    for (var i = 0; i < options.length; i++) {
      byLabel[options[i].label] = options[i];
    }

    var form = input.form;
    if (!form) return;

    input.addEventListener("input", function () {
      var picked = byLabel[input.value];
      if (!picked) return;
      var jidField = form.querySelector("[name=group_jid]");
      var nameField = form.querySelector("[name=name]");
      if (jidField) jidField.value = picked.jid;
      if (nameField) nameField.value = picked.name;
    });
  }

  function init() {
    var pickers = document.querySelectorAll("[data-known-group-picker]");
    for (var i = 0; i < pickers.length; i++) {
      wire(pickers[i]);
    }
  }

  document.addEventListener("DOMContentLoaded", init);
  document.addEventListener("htmx:afterSwap", init);
})();
