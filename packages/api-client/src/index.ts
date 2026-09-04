import createClient from "openapi-fetch";

import type { paths } from "./generated/schema.js";

export type { components, paths } from "./generated/schema.js";

export function createApiClient(baseUrl: string) {
  return createClient<paths>({ baseUrl });
}
