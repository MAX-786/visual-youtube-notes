// URL -> YouTube video ID, for every form spec 8.1 accepts: watch?v=,
// m.youtube.com, youtu.be/, /shorts/, /live/, /embed/, and playlist URLs
// carrying v. Anything else is null, never a guess.

const ID = /^[A-Za-z0-9_-]{11}$/;

const HOSTS = new Set([
  "www.youtube.com",
  "youtube.com",
  "m.youtube.com",
  "music.youtube.com",
  "www.youtube-nocookie.com",
  "youtube-nocookie.com",
]);

export function videoIdFromUrl(raw: string): string | null {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") return null;
  const host = url.hostname.toLowerCase();
  const path = url.pathname.replace(/\/+$/, "");
  if (host === "youtu.be") {
    return check(path.split("/")[1]);
  }
  if (!HOSTS.has(host)) return null;
  if (path === "/watch") {
    return check(url.searchParams.get("v"));
  }
  const m = path.match(/^\/(shorts|live|embed|v)\/([^/]+)/);
  if (m) return check(m[2]);
  if (path === "/playlist" || path === "/attribution_link") {
    return check(url.searchParams.get("v"));
  }
  return null;
}

function check(candidate: string | null | undefined): string | null {
  if (!candidate) return null;
  return ID.test(candidate) ? candidate : null;
}
