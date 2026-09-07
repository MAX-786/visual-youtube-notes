import { describe, expect, it } from "vitest";
import { apiRequestInit } from "../src/lib/request.js";

describe("apiRequestInit", () => {
  // The defect this guards: Chrome attaches the app's session cookie to an
  // extension fetch whose host is in host_permissions, and the API refuses a
  // request carrying a cookie and a bearer token together (ADR 0003).
  it.each([
    ["GET", undefined, undefined],
    ["POST", { video_id: "aircAruvnKk" }, "token"],
    ["POST", undefined, "token"],
    ["DELETE", undefined, undefined],
  ])("omits credentials for %s", (method, body, token) => {
    expect(apiRequestInit(method, body, token).credentials).toBe("omit");
  });

  it("carries the bearer token only when there is one", () => {
    const withToken = apiRequestInit("GET", undefined, "abc123")
      .headers as Record<string, string>;
    expect(withToken.Authorization).toBe("Bearer abc123");
    const without = apiRequestInit("GET").headers as Record<string, string>;
    expect(without.Authorization).toBeUndefined();
    expect(without.Accept).toBe("application/json");
  });

  it("declares and serializes JSON only when there is a body", () => {
    const withBody = apiRequestInit("POST", { video_id: "aircAruvnKk" });
    expect((withBody.headers as Record<string, string>)["Content-Type"]).toBe(
      "application/json",
    );
    expect(withBody.body).toBe('{"video_id":"aircAruvnKk"}');
    const without = apiRequestInit("POST");
    expect(
      (without.headers as Record<string, string>)["Content-Type"],
    ).toBeUndefined();
    expect(without.body).toBeUndefined();
  });
});
