import createClient from "openapi-fetch";

import type { paths } from "./generated/schema.js";

export type { components, paths } from "./generated/schema.js";
export type { Middleware } from "openapi-fetch";

export function createApiClient(baseUrl: string) {
  return createClient<paths>({ baseUrl, credentials: "same-origin" });
}
