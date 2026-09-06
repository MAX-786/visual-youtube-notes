// Where the in-page button attaches: the watch-page actions row so it sits
// with Like and Share, else a floating overlay anchored to the player.
// YouTube renames its element IDs without notice, so a selector that
// degrades to a working overlay beats one that degrades to nothing.

export type Anchor =
  { kind: "actions"; host: Element } | { kind: "overlay"; host: Element };

const ACTION_ROW_SELECTORS = [
  "#actions #top-level-buttons-computed",
  "#actions-inner #top-level-buttons-computed",
  "ytd-watch-metadata #actions #top-level-buttons-computed",
  "#top-level-buttons-computed",
  "ytd-menu-renderer #top-level-buttons-computed",
];

const PLAYER_SELECTORS = [
  "#movie_player",
  "#player-container",
  "#player",
  "ytd-player",
];

export function resolveAnchor(root: ParentNode = document): Anchor | null {
  for (const selector of ACTION_ROW_SELECTORS) {
    const host = root.querySelector(selector);
    if (host && isVisible(host)) return { kind: "actions", host };
  }
  for (const selector of PLAYER_SELECTORS) {
    const host = root.querySelector(selector);
    if (host) return { kind: "overlay", host };
  }
  const body = (root as Document).body ?? root.querySelector("body");
  return body ? { kind: "overlay", host: body } : null;
}

function isVisible(el: Element): boolean {
  if (!(el instanceof HTMLElement)) return true;
  if (typeof getComputedStyle !== "function") return true;
  const style = getComputedStyle(el);
  return style.display !== "none" && style.visibility !== "hidden";
}
