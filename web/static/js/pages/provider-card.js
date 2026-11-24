// What the provider list and a provider's own page share: the words for a
// custom endpoint, the check that a key change invalidates, and the results
// dialog. Mixed into both page components.
import { t } from "/static/js/ui/i18n.js";

// nodeTypeLabel is the words for a custom endpoint's kind.
function nodeTypeLabel(node) {
  if (!node) {
    return "";
  }
  if (node.type === "anthropic-compatible") {
    return t("providers.nodeTypes.anthropic");
  }
  if (node.type === "custom-embedding") {
    return t("providers.nodeTypes.embedding");
  }
  return node.apiType === "responses" ? t("providers.nodeTypes.responses") : t("providers.nodeTypes.openai");
}

export const cardMethods = {
  nodeLabel(card) {
    return nodeTypeLabel(card.entry.node);
  },

  // A key or model that changed is no longer the one that was checked.
  setCheckedField(path, event) {
    this.setField(path, event);
    this.add.check = null;
  },

  closeResults() {
    this.$refs.resultsDlg.close();
    this.results = null;
  },
};
