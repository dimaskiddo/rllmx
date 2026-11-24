// The usage page's pictures: the trend line, the two bar charts and the
// provider ring. Chart.js draws a canvas; the ring is drawn node by node, so
// nothing here puts data into markup.
import { makeChart, destroyChart, chartPalette } from "/static/js/ui/charts.js";

const svgNS = "http://www.w3.org/2000/svg";

function fade(hex) {
  const value = hex.replace("#", "");
  if (value.length !== 6) {
    return "rgba(32, 107, 196, 0.15)";
  }
  const r = parseInt(value.slice(0, 2), 16);
  const g = parseInt(value.slice(2, 4), 16);
  const b = parseInt(value.slice(4, 6), 16);
  return "rgba(" + r + ", " + g + ", " + b + ", 0.15)";
}

function chartOptions(colors, horizontal) {
  const axis = { grid: { color: colors.grid }, ticks: { color: colors.text } };
  return {
    responsive: true,
    maintainAspectRatio: false,
    indexAxis: horizontal ? "y" : "x",
    plugins: { legend: { display: false } },
    scales: { x: axis, y: Object.assign({}, axis, { beginAtZero: true }) },
  };
}

export const usageChartMethods = {
  drawTrend() {
    const canvas = this.$refs.trend;
    if (!canvas) {
      return;
    }
    const colors = chartPalette();
    const key = this.metric;
    makeChart(canvas, {
      type: "line",
      data: {
        labels: this.points.map((row) => row.label),
        datasets: [{ label: this.label("usage." + key), data: this.points.map((row) => row[key]), borderColor: colors.line, backgroundColor: fade(colors.line), fill: true, tension: 0.3, borderWidth: 2, pointRadius: 0 }],
      },
      options: chartOptions(colors, false),
    });
  },

  drawBars() {
    const colors = chartPalette();
    const metric = this.barMetric;
    const valueOf = (entry) => (metric === "requests" ? entry.requests : entry.promptTokens + entry.completionTokens);
    const providers = this.topEntries(this.stats.byProvider, valueOf, 8);
    const models = this.topEntries(this.stats.byModel, valueOf, 5);
    this.paintBars(this.$refs.providerBars, providers, colors, metric);
    this.paintBars(this.$refs.modelBars, models, colors, metric);
  },

  topEntries(table, valueOf, limit) {
    const rows = Object.entries(table || {}).map(([key, entry]) => ({ key: key, label: entry.rawModel || key, value: valueOf(entry) }));
    rows.sort((a, b) => b.value - a.value);
    return rows.slice(0, limit);
  },

  paintBars(canvas, rows, colors, metric) {
    if (!canvas) {
      return;
    }
    makeChart(canvas, {
      type: "bar",
      data: {
        labels: rows.map((row) => row.label),
        datasets: [{ label: this.label("usage." + metric), data: rows.map((row) => row.value), backgroundColor: rows.map((row, i) => colors.bars[i % colors.bars.length]), borderRadius: 3 }],
      },
      options: chartOptions(colors, true),
    });
  },

  // drawTopology draws the router at the centre and one provider per point on
  // an ellipse. Each node's class comes from the live feed, so a provider that
  // is answering, one used last and one failing look different.
  drawTopology() {
    const svg = this.$refs.topology;
    // A redraw replaces every node, which would drop the focus of someone
    // tabbing through them; the next frame draws it.
    if (!svg || svg.contains(document.activeElement)) {
      return;
    }
    const providers = this.topologyProviders();
    const width = 640;
    const height = 360;
    const cx = width / 2;
    const cy = height / 2;
    const rx = width / 2 - 90;
    const ry = height / 2 - 56;
    const edges = document.createElementNS(svgNS, "g");
    const nodes = document.createElementNS(svgNS, "g");
    const router = document.createElementNS(svgNS, "g");
    router.setAttribute("class", "rl-topo-router");
    const routerCircle = document.createElementNS(svgNS, "circle");
    routerCircle.setAttribute("cx", String(cx));
    routerCircle.setAttribute("cy", String(cy));
    routerCircle.setAttribute("r", "26");
    const routerText = document.createElementNS(svgNS, "text");
    routerText.setAttribute("x", String(cx));
    routerText.setAttribute("y", String(cy + 4));
    routerText.setAttribute("text-anchor", "middle");
    routerText.textContent = "R";
    router.append(routerCircle, routerText);

    providers.forEach((provider, i) => {
      const angle = (2 * Math.PI * i) / providers.length - Math.PI / 2;
      const x = cx + rx * Math.cos(angle);
      const y = cy + ry * Math.sin(angle);
      const edge = document.createElementNS(svgNS, "line");
      edge.setAttribute("x1", String(cx));
      edge.setAttribute("y1", String(cy));
      edge.setAttribute("x2", String(x));
      edge.setAttribute("y2", String(y));
      edge.setAttribute("class", this.nodeState(provider));
      edges.append(edge);

      const node = document.createElementNS(svgNS, "g");
      node.setAttribute("class", "rl-topo-node " + this.nodeState(provider));
      node.setAttribute("tabindex", "0");
      node.setAttribute("role", "link");
      node.setAttribute("aria-label", provider + ", " + this.label("usage.state." + (this.nodeState(provider) || "idle")));
      const dot = document.createElementNS(svgNS, "circle");
      dot.setAttribute("cx", String(x));
      dot.setAttribute("cy", String(y));
      dot.setAttribute("r", "10");
      const label = document.createElementNS(svgNS, "text");
      label.setAttribute("x", String(x));
      label.setAttribute("y", String(y + 24));
      label.setAttribute("text-anchor", "middle");
      label.textContent = provider.length > 16 ? provider.slice(0, 14) + "…" : provider;
      node.append(dot, label);
      node.addEventListener("click", () => {
        window.location.assign("/dashboard/providers/" + encodeURIComponent(provider));
      });
      node.addEventListener("keydown", (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          window.location.assign("/dashboard/providers/" + encodeURIComponent(provider));
        }
      });
      nodes.append(node);
    });
    svg.replaceChildren(edges, router, nodes);
  },

  topologyProviders() {
    const names = Object.keys(this.stats.byProvider || {});
    for (const row of this.live.activeRequests || []) {
      if (names.indexOf(row.provider) < 0) {
        names.push(row.provider);
      }
    }
    return names.slice(0, 12);
  },

  nodeState(provider) {
    if (provider === this.live.errorProvider) {
      return "is-error";
    }
    if (this.activeProviders.indexOf(provider) >= 0) {
      return "is-active";
    }
    if (this.lastProvider === provider) {
      return "is-last";
    }
    return "";
  },

  drawAll() {
    this.drawTrend();
    this.drawBars();
    this.drawTopology();
  },

  dropCharts() {
    destroyChart(this.$refs.trend);
    destroyChart(this.$refs.providerBars);
    destroyChart(this.$refs.modelBars);
  },
};
