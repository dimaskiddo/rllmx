// x-t, x-icon and x-tip take a word, not an expression: the directive reads the raw
// attribute, so nothing is evaluated and a translated key can never run code.
// Every page that uses them imports this file before Alpine starts, because an
// unregistered directive is a silent no-op.
import Alpine from "/static/lib/alpine.csp.js";
import { t } from "./i18n.js";
import { icon } from "./icons.js";

Alpine.directive("t", (el, { expression }) => {
  el.textContent = t(expression.trim());
});

Alpine.directive("icon", (el, { expression }) => {
  icon(el, expression.trim());
});

// x-tip is the hover text of a button that shows only an icon.
Alpine.directive("tip", (el, { expression }) => {
  el.title = t(expression.trim());
});
