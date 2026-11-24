// Small presentation helpers every page needs. They hold no state, so a page
// component mixes them in with Object.assign and calls them from its markup.
import { t } from "./i18n.js";

const compactUnits = [
  { value: 1e12, suffix: "T" },
  { value: 1e9, suffix: "B" },
  { value: 1e6, suffix: "M" },
  { value: 1e3, suffix: "K" },
];

// compactTokens writes a count the way a chart axis wants it: 1.5K, 2.0M, 3.0B,
// 1.0T. A figure that rounds up to 1000 of its unit is written in the next unit
// up, so 999,950 reads 1.0M and not 1000.0K.
export function compactTokens(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n === 0) {
    return "0";
  }
  const abs = Math.abs(n);
  const sign = n < 0 ? "-" : "";
  for (let i = 0; i < compactUnits.length; i++) {
    const unit = compactUnits[i];
    if (abs < unit.value) {
      continue;
    }
    if (Number((abs / unit.value).toFixed(1)) >= 1000 && i > 0) {
      const up = compactUnits[i - 1];
      return sign + (abs / up.value).toFixed(1) + up.suffix;
    }
    return sign + (abs / unit.value).toFixed(1) + unit.suffix;
  }
  return Math.round(abs) >= 1000 ? sign + "1.0K" : String(n);
}

export const formatMethods = {
  // formatDate writes a stored timestamp in the reader's own zone and format,
  // and leaves a value it cannot read as it was.
  formatDate(value) {
    if (!value) {
      return "";
    }
    const date = new Date(value);
    return isNaN(date.getTime()) ? value : date.toLocaleString();
  },

  // tagLabel says in words what a failure tag from the store stands for. A
  // status code with no wording of its own is shown as the code.
  tagLabel(tag) {
    if (!tag) {
      return "";
    }
    const text = t("tag." + tag);
    if (text !== "tag." + tag) {
      return text;
    }
    return /^[0-9]+$/.test(tag) ? t("tag.http") + " " + tag : tag;
  },

  // statusName is the connection status in words; a status the page has no
  // wording for is shown as the store wrote it.
  statusName(status) {
    const text = t("provider.status." + status);
    return text === "provider.status." + status ? status : text;
  },

  // num groups digits the way the reader's locale does.
  num(value) {
    return Number(value || 0).toLocaleString();
  },

  // meterLevel colours a remaining-capacity meter: red under 30%, amber under
  // 70%. The quota page and a key's budget read the same way.
  meterLevel(percent) {
    if (percent < 30) {
      return "low";
    }
    return percent < 70 ? "mid" : "";
  },

  // label is t() for markup whose key is chosen at render.
  label(key) {
    return t(key);
  },
};
