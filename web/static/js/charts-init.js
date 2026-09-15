/* Sismaya Lite — ECharts hydration.
 * Reads each <script type="application/json" data-chart="KEY"> option blob and
 * renders it into #chart-KEY. Registers light/dark themes from the minimal
 * palette so charts follow the page theme. Re-runs on htmx:afterSwap and
 * disposes stale instances first (avoids a canvas leak per swap). */
(function () {
  'use strict';
  if (typeof echarts === 'undefined') return;

  // palette mirrors minimal.css :root accent colours.
  var SERIES = ['#00A76F', '#00B8D9', '#8E33FF', '#FFAB00', '#FF5630', '#22C55E'];

  echarts.registerTheme('sismaya-light', {
    color: SERIES,
    backgroundColor: 'transparent',
    textStyle: { color: '#637381' },
  });
  echarts.registerTheme('sismaya-dark', {
    color: SERIES,
    backgroundColor: 'transparent',
    textStyle: { color: '#919EAB' },
  });

  function activeTheme() {
    return document.documentElement.getAttribute('data-theme') === 'dark'
      ? 'sismaya-dark'
      : 'sismaya-light';
  }

  function initCharts() {
    var theme = activeTheme();
    document.querySelectorAll('[data-chart]').forEach(function (blob) {
      var el = document.getElementById('chart-' + blob.dataset.chart);
      if (!el) return;
      var option;
      try {
        option = JSON.parse(blob.textContent);
      } catch (e) {
        return;
      }
      var existing = echarts.getInstanceByDom(el);
      if (existing) existing.dispose(); // theme can only be set at init time
      echarts.init(el, theme).setOption(option);
    });
  }

  function resizeCharts() {
    document.querySelectorAll('[data-chart]').forEach(function (blob) {
      var el = document.getElementById('chart-' + blob.dataset.chart);
      var inst = el && echarts.getInstanceByDom(el);
      if (inst) inst.resize();
    });
  }

  document.addEventListener('DOMContentLoaded', initCharts);
  document.addEventListener('htmx:afterSwap', initCharts);
  window.addEventListener('resize', resizeCharts);

  // Re-hydrates every chart on demand — used when a chart's container was
  // hidden (e.g. inside a <dialog> that starts closed) when DOMContentLoaded
  // first ran, so echarts.init saw a 0x0 canvas. modal.js dispatches this
  // once a <dialog> containing a chart actually opens. Calling initCharts
  // again is safe: it disposes any existing instance per element first.
  document.addEventListener('wsm:charts-refresh', initCharts);
})();
