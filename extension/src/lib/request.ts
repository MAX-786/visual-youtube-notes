// How the extension shapes every call to the API (ADR 0013): a bearer
// token and nothing else.
//
// The credentials mode is pinned to "omit" rather than left to fetch's
// default. An extension whose host_permissions cover the API's origin gets
// its requests treated as first-party by Chrome, so the app's own session
// cookie rides along with a fetch from the service worker; the API refuses
// a request carrying both a session cookie and a bearer token (ADR 0003,
// mixed_transport), and the Owner meets it as "Send either the session
// cookie or a bearer token, never both" the first time they save while
// signed in to the app in the same browser.

/** The RequestInit for one API call: bearer only, never a cookie. */
export function apiRequestInit(
  method: string,
  body?: unknown,
  token?: string,
): RequestInit {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (token) headers.Authorization = `Bearer ${token}`;
  return {
    method,
    headers,
    credentials: "omit",
    body: body === undefined ? undefined : JSON.stringify(body),
  };
}
