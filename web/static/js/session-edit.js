// /sessions' Edit/Delete dialogs (Part 3 redesign). Plain IIFE over the
// DOM, same shape as nav-drawer.js/known-groups.js — no framework, no
// build step.
//
// Three independent jobs:
//
//  1. Auto-open: a dialog swapped into #session-edit-slot (by hx-get on
//     Edit/Delete…, or re-rendered in place by a POST's response) starts
//     life CLOSED — innerHTML swap always creates a fresh <dialog> node,
//     even on a validation-error re-render — so anything carrying
//     data-autoopen gets .showModal() called on it once it lands. Mirrors
//     modal.js's own "wait a frame, then act" pattern for a freshly
//     swapped-in dialog.
//  2. Group chips: the visible text <input data-chip-input> turns Enter or
//     a comma into a chip, each chip's × removes it, and the chips'
//     combined value is kept in a hidden <input name="group_codes"> as a
//     comma-separated string — the exact shape PostSessionEdit's
//     csvToGroupCodes expects.
//  3. Delete type-to-confirm: the danger button inside the delete dialog
//     stays disabled until the operator types the session's own name into
//     the confirmation field.
//
// Dialog CLOSE on a successful save/delete piggybacks on the SAME
// "sessions-changed" HX-Trigger the polled tbody already listens for
// (list.html's hx-trigger="…, sessions-changed from:body") — see the
// document-level listener at the bottom of this file. A validation-error
// response never sets that header, so a failed save correctly leaves the
// dialog open with the error inline.
(function () {
  "use strict";

  function autoOpen(root) {
    var dialogs = root.querySelectorAll("dialog[data-autoopen]");
    for (var i = 0; i < dialogs.length; i++) {
      var d = dialogs[i];
      if (typeof d.showModal === "function" && !d.open) {
        d.showModal();
      }
    }
  }

  function wireChips(scope) {
    var groups = scope.querySelectorAll("[data-chip-group]");
    for (var g = 0; g < groups.length; g++) {
      wireOneChipGroup(groups[g]);
    }
  }

  function wireOneChipGroup(group) {
    var input = group.querySelector("[data-chip-input]");
    var hidden = group.querySelector("[data-chip-hidden]");
    var list = group.querySelector("[data-chip-list]");
    var emptyNote = group.querySelector("[data-chip-empty]");
    if (!input || !hidden || !list) return;

    function current() {
      return hidden.value ? hidden.value.split(",").filter(Boolean) : [];
    }

    function render(codes) {
      hidden.value = codes.join(",");
      list.querySelectorAll(".se-chip").forEach(function (el) { el.remove(); });
      for (var i = 0; i < codes.length; i++) {
        list.insertBefore(buildChip(codes[i]), input);
      }
      if (emptyNote) emptyNote.hidden = codes.length > 0;
    }

    function buildChip(code) {
      var chip = document.createElement("span");
      chip.className = "se-chip";
      var text = document.createElement("span");
      text.textContent = code;
      chip.appendChild(text);
      var remove = document.createElement("button");
      remove.type = "button";
      remove.className = "se-chip-remove";
      remove.setAttribute("aria-label", "Remove " + code);
      remove.textContent = "×";
      remove.addEventListener("click", function () {
        render(current().filter(function (c) { return c !== code; }));
        input.focus();
      });
      chip.appendChild(remove);
      return chip;
    }

    function add(raw) {
      var code = raw.trim().toLowerCase();
      if (!code) return;
      var codes = current();
      if (codes.indexOf(code) === -1) {
        codes.push(code);
        render(codes);
      }
      input.value = "";
    }

    input.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === ",") {
        e.preventDefault();
        add(input.value);
      } else if (e.key === "Backspace" && input.value === "") {
        var codes = current();
        if (codes.length > 0) {
          render(codes.slice(0, -1));
        }
      }
    });
    input.addEventListener("blur", function () {
      if (input.value.trim() !== "") add(input.value);
    });

    // Initial paint from the hidden field's prefilled value.
    render(current());
  }

  function wireDeleteConfirm(scope) {
    var guards = scope.querySelectorAll("[data-delete-confirm]");
    for (var i = 0; i < guards.length; i++) {
      wireOneDeleteConfirm(guards[i]);
    }
  }

  function wireOneDeleteConfirm(el) {
    var input = el.querySelector("[data-delete-confirm-input]");
    var button = el.querySelector("[data-delete-confirm-button]");
    var want = el.getAttribute("data-delete-confirm");
    if (!input || !button || !want) return;
    function check() {
      button.disabled = input.value !== want;
    }
    input.addEventListener("input", check);
    check();
  }

  function init(root) {
    root = root || document;
    autoOpen(root);
    wireChips(root);
    wireDeleteConfirm(root);
  }

  document.addEventListener("DOMContentLoaded", function () { init(document); });
  document.addEventListener("htmx:afterSwap", function (e) {
    if (e.detail && e.detail.target) init(e.detail.target);
  });

  // The tbody's own refresh (list.html's hx-trigger) also fires on this
  // event (session_rows.html listens "sessions-changed from:body"); a
  // successful edit/delete save response ALSO carries HX-Trigger:
  // sessions-changed on the dialog's own form/button, so this is the one
  // place that closes whichever dialog is currently open after a save —
  // no separate "close" signal needed.
  document.addEventListener("sessions-changed", function () {
    document.querySelectorAll("dialog.modal[open]").forEach(function (d) {
      d.close();
    });
  });
})();
