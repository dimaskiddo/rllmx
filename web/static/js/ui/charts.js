// Chart.js arrives as a classic UMD script, so it is a global. Every chart is
// built and destroyed by the page's own methods, because an Alpine expression
// has nowhere to hold the instance.

const held = new Map();

function token(name, fallback) {
  const value = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return value || fallback;
}

// chartPalette is read at draw time. Text and grid come from plain tokens
// because a canvas cannot parse the light-dark() Tabler declares its own with.
export function chartPalette() {
  return {
    text: token("--rl-chart-text", "#3f3f46"),
    grid: token("--rl-chart-grid", "#e4e4e7"),
    line: token("--tblr-primary", "#206bc4"),
    bars: [token("--tblr-primary", "#206bc4"), token("--tblr-teal", "#0ca678"), token("--tblr-azure", "#4299e1"), token("--tblr-orange", "#f76707"), token("--tblr-purple", "#ae3ec9")],
  };
}

export function makeChart(canvas, config) {
  const Chart = globalThis.Chart;
  if (!Chart || !canvas) {
    return null;
  }
  destroyChart(canvas);
  const chart = new Chart(canvas, config);
  held.set(canvas, chart);
  return chart;
}

export function destroyChart(canvas) {
  const chart = held.get(canvas);
  if (!chart) {
    return;
  }
  chart.destroy();
  held.delete(canvas);
}
