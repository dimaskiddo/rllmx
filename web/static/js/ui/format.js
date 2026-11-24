// Small presentation helpers every page needs. They hold no state, so a page
// component mixes them in with Object.assign and calls them from its markup.
import { t } from "./i18n.js";

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

  // label is t() for markup whose key is chosen at render.
  label(key) {
    return t(key);
  },
};
