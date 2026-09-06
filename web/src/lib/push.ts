// Web Push on the client (ADR 0014): permission only from an explicit
// press, the subscription registered against the PWA session, and the two
// switches. The service worker shows and routes the notifications.
import { api } from "./api";
import type { components } from "@visual-notes/api-client";

export type PushSubscriptionRow = components["schemas"]["PushSubscription"];

export function pushSupported(): boolean {
  return (
    typeof window !== "undefined" &&
    "serviceWorker" in navigator &&
    "PushManager" in window &&
    "Notification" in window
  );
}

function toBase64url(buffer: ArrayBuffer | null): string {
  if (!buffer) return "";
  let s = "";
  for (const b of new Uint8Array(buffer)) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64url(s: string): Uint8Array {
  const padded =
    s.replace(/-/g, "+").replace(/_/g, "/") +
    "=".repeat((4 - (s.length % 4)) % 4);
  const raw = atob(padded);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

/** The browser's current subscription for this origin, if any. */
export async function currentSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null;
  const registration = await navigator.serviceWorker.ready;
  return registration.pushManager.getSubscription();
}

/**
 * Asks permission (this is the only place that may), subscribes with the
 * server's public key, and registers the endpoint against this session.
 */
export async function enablePush(): Promise<
  { ok: true; row: PushSubscriptionRow } | { ok: false; reason: string }
> {
  if (!pushSupported())
    return {
      ok: false,
      reason: "This browser cannot receive push notifications.",
    };
  const permission = await Notification.requestPermission();
  if (permission !== "granted")
    return { ok: false, reason: "Notifications were not allowed." };
  const key = await api.GET("/push/vapid-public-key");
  if (!key.data)
    return { ok: false, reason: "Web Push is not configured on the server." };
  const registration = await navigator.serviceWorker.ready;
  let subscription = await registration.pushManager.getSubscription();
  if (!subscription) {
    subscription = await registration.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: fromBase64url(key.data.public_key) as BufferSource,
    });
  }
  const row = await register(subscription, null);
  if (!row)
    return { ok: false, reason: "The subscription could not be saved." };
  return { ok: true, row };
}

/** Registers (or swaps) a subscription on the server. */
export async function register(
  subscription: PushSubscription,
  replaces: string | null,
): Promise<PushSubscriptionRow | null> {
  const { data } = await api.PUT("/push-subscriptions", {
    body: {
      endpoint: subscription.endpoint,
      keys: {
        p256dh: toBase64url(subscription.getKey("p256dh")),
        auth: toBase64url(subscription.getKey("auth")),
      },
      expiration_time: subscription.expirationTime
        ? new Date(subscription.expirationTime).toISOString()
        : null,
      notes_enabled: true,
      ops_enabled: true,
      ...(replaces ? { replaces_endpoint: replaces } : {}),
    },
  });
  return data ?? null;
}

/** Unsubscribes this browser and removes its row. */
export async function disablePush(
  row: PushSubscriptionRow | null,
): Promise<void> {
  const subscription = await currentSubscription();
  if (subscription) await subscription.unsubscribe().catch(() => false);
  if (row)
    await api.DELETE("/push-subscriptions/{subscriptionId}", {
      params: { path: { subscriptionId: row.id } },
    });
}
