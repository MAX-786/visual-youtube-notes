// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { resolveAnchor } from "../src/lib/anchor.js";

// A captured slice of a watch page: the actions row with Like and Share,
// inside the metadata renderer, beside the player.
const watchPage = `
<div id="page-manager">
  <div id="player-container"><div id="movie_player"></div></div>
  <ytd-watch-metadata>
    <div id="actions">
      <div id="actions-inner">
        <ytd-menu-renderer>
          <div id="top-level-buttons-computed">
            <button>Like</button>
            <button>Share</button>
          </div>
        </ytd-menu-renderer>
      </div>
    </div>
  </ytd-watch-metadata>
</div>`;

describe("resolveAnchor", () => {
  it("attaches to the actions row beside Like and Share", () => {
    document.body.innerHTML = watchPage;
    const anchor = resolveAnchor();
    expect(anchor?.kind).toBe("actions");
    expect(anchor?.host.id).toBe("top-level-buttons-computed");
  });

  it("falls back to an overlay on the player when the actions row is gone", () => {
    document.body.innerHTML = watchPage.replace(
      /<div id="actions">[\s\S]*?<\/ytd-watch-metadata>/,
      "</ytd-watch-metadata>",
    );
    const anchor = resolveAnchor();
    expect(anchor?.kind).toBe("overlay");
    expect(anchor?.host.id).toBe("movie_player");
  });

  it("falls back to the body when even the player is missing", () => {
    document.body.innerHTML = "<div>nothing here</div>";
    const anchor = resolveAnchor();
    expect(anchor?.kind).toBe("overlay");
    expect(anchor?.host.tagName).toBe("BODY");
  });
});
