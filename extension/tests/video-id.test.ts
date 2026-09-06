import { describe, expect, it } from "vitest";
import { videoIdFromUrl } from "../src/lib/video-id.js";

describe("videoIdFromUrl", () => {
  const id = "aircAruvnKk";
  it.each([
    [`https://www.youtube.com/watch?v=${id}`, id],
    [`https://www.youtube.com/watch?v=${id}&t=42s&list=PLx`, id],
    [`https://youtube.com/watch?v=${id}`, id],
    [`https://m.youtube.com/watch?v=${id}`, id],
    [`https://youtu.be/${id}`, id],
    [`https://youtu.be/${id}?t=10`, id],
    [`https://www.youtube.com/shorts/${id}`, id],
    [`https://www.youtube.com/live/${id}`, id],
    [`https://www.youtube.com/embed/${id}?autoplay=1`, id],
    [`https://www.youtube-nocookie.com/embed/${id}`, id],
    [`https://www.youtube.com/playlist?list=PLx&v=${id}`, id],
    [`https://www.youtube.com/v/${id}`, id],
  ])("accepts %s", (url, expected) => {
    expect(videoIdFromUrl(url)).toBe(expected);
  });

  it.each([
    "https://vimeo.com/12345",
    "https://www.youtube.com/",
    "https://www.youtube.com/feed/subscriptions",
    "https://www.youtube.com/watch",
    "https://www.youtube.com/watch?v=tooshort",
    "https://www.youtube.com/watch?v=way-too-long-id-here",
    "https://www.youtube.com/playlist?list=PLx",
    "https://evil.example/youtube.com/watch?v=aircAruvnKk",
    "ftp://www.youtube.com/watch?v=aircAruvnKk",
    "not a url",
    "",
  ])("rejects %s", (url) => {
    expect(videoIdFromUrl(url)).toBeNull();
  });
});
