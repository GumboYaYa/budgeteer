// Charts on the overview page. Data comes from the #chart-data JSON block;
// colors come from CSS custom properties so light and dark mode each get
// their own validated step (see assets/app.css).
(function () {
  "use strict";
  const dataEl = document.getElementById("chart-data");
  if (!dataEl || !window.Chart) return;
  const data = JSON.parse(dataEl.textContent);

  const css = getComputedStyle(document.documentElement);
  const color = (name) => css.getPropertyValue(name).trim();
  const ink = color("--chart-ink");
  const grid = color("--chart-grid");
  const surface = color("--chart-surface");

  const euro = new Intl.NumberFormat("de-DE", { style: "currency", currency: "EUR" });
  const euroShort = new Intl.NumberFormat("de-DE", { maximumFractionDigits: 0 });

  Chart.defaults.color = ink;
  Chart.defaults.font.family = getComputedStyle(document.body).fontFamily;
  Chart.defaults.animation = false;
  Chart.defaults.maintainAspectRatio = false;

  const valueAxis = {
    beginAtZero: true,
    border: { display: false },
    grid: { color: grid },
    ticks: { callback: (v) => euroShort.format(v) + " €", maxTicksLimit: 6 },
  };
  const categoryAxis = { border: { display: false }, grid: { display: false } };
  // 2px surface-colored border separates neighbouring bars.
  const bar = { borderRadius: 4, borderWidth: 2, borderColor: surface, borderSkipped: "start" };

  const spending = document.getElementById("chart-spending");
  if (spending) {
    new Chart(spending, {
      type: "bar",
      data: {
        labels: data.spending.labels,
        datasets: [{ label: "Spending", data: data.spending.values, backgroundColor: color("--series-1"), maxBarThickness: 18, ...bar }],
      },
      options: {
        indexAxis: "y",
        scales: { x: valueAxis, y: categoryAxis },
        plugins: {
          legend: { display: false }, // single series; the heading names it
          tooltip: { callbacks: { label: (c) => euro.format(c.parsed.x) } },
        },
      },
    });
  }

  const trend = document.getElementById("chart-trend");
  if (trend) {
    new Chart(trend, {
      type: "bar",
      data: {
        labels: data.trend.labels,
        datasets: [
          { label: "Income", data: data.trend.income, backgroundColor: color("--series-1"), maxBarThickness: 22, ...bar },
          { label: "Spending", data: data.trend.spending, backgroundColor: color("--series-2"), maxBarThickness: 22, ...bar },
        ],
      },
      options: {
        scales: { x: categoryAxis, y: valueAxis },
        interaction: { mode: "index", intersect: false },
        plugins: {
          legend: { position: "top", align: "start", labels: { boxWidth: 12, boxHeight: 12, useBorderRadius: true, borderRadius: 3 } },
          tooltip: { callbacks: { label: (c) => c.dataset.label + ": " + euro.format(c.parsed.y) } },
        },
      },
    });
  }
})();
