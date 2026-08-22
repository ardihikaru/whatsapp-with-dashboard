// Off-canvas navigation drawer for phone portrait.
//
// The markup in base.html is the SAME sidebar the desktop uses. CSS decides
// which of two shapes it takes below 900px, and this file picks the shape by
// setting data-nav-js on <html>: without it the sidebar renders as an ordinary
// block above the content, so the menu is reachable even if this script never
// runs. With it, the sidebar becomes a drawer driven by data-nav-open.
//
// No library. This mirrors charts-init.js: a plain IIFE over the DOM.
(function () {
  "use strict";

  var root = document.documentElement;
  var toggle = document.querySelector(".nav-toggle");
  var sidebar = document.getElementById("app-sidebar");
  var backdrop = document.querySelector(".nav-backdrop");
  if (!toggle || !sidebar) return;

  // Only now does the drawer CSS apply, and only now is the hamburger shown.
  root.setAttribute("data-nav-js", "");
  if (backdrop) backdrop.removeAttribute("hidden");

  // Matches the max-width: 900px breakpoint in responsive.css. Kept in one
  // place here so the two cannot drift apart silently.
  var phone = window.matchMedia("(max-width: 900px)");

  function isOpen() {
    return root.hasAttribute("data-nav-open");
  }

  function syncInert() {
    // Off-canvas and off the tab order. On desktop the sidebar is in normal
    // flow and must never be inert.
    if (phone.matches && !isOpen()) sidebar.setAttribute("inert", "");
    else sidebar.removeAttribute("inert");
  }

  function open() {
    root.setAttribute("data-nav-open", "");
    toggle.setAttribute("aria-expanded", "true");
    syncInert();
    var first = sidebar.querySelector(".nav-item");
    if (first) first.focus();
  }

  function close(returnFocus) {
    root.removeAttribute("data-nav-open");
    toggle.setAttribute("aria-expanded", "false");
    syncInert();
    if (returnFocus) toggle.focus();
  }

  toggle.addEventListener("click", function () {
    if (isOpen()) close(true);
    else open();
  });

  if (backdrop) {
    backdrop.addEventListener("click", function () {
      close(false);
    });
  }

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && isOpen()) close(true);
  });

  // A nav link is a full page load, but the drawer must close before it starts
  // or the next page paints behind a stale backdrop on a slow connection.
  sidebar.addEventListener("click", function (e) {
    if (e.target.closest(".nav-item")) close(false);
  });

  // Rotating to landscape or crossing into desktop leaves data-nav-open set on
  // a sidebar that is now part of the layout; the backdrop would stay armed.
  function onBreakpoint() {
    if (!phone.matches && isOpen()) close(false);
    syncInert();
  }
  if (phone.addEventListener) phone.addEventListener("change", onBreakpoint);
  else phone.addListener(onBreakpoint); // Safari < 14

  syncInert();
})();
