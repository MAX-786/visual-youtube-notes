# Let the Chrome Web Store own the production extension ID

ADR 0013 chose an unpacked-only distribution model and committed a manifest `key` to pin the
extension ID. The product is now being distributed through the Chrome Web Store, whose upload
validator rejects that field in the submitted package. This ADR supersedes only ADR 0013's identity,
packaging, and distribution decision. Its content-script, service-worker, pairing, token, and
message-boundary decisions remain current.

## Decision

`extension/manifest.json` is the canonical Web Store release manifest. It contains neither `key`
nor `update_url`. The release build refuses either field, places `manifest.json` at the ZIP root,
and names the artifact with the manifest version.

The Chrome Web Store assigns the production Item ID. The deployed API receives that value through
`CHROME_EXTENSION_ID` in `deploy/.env`, passed to the process as `VYN_EXTENSION_ID`. That one value
continues to define both the CORS allowlist and the extension ID accepted by pairing.

For an unpacked build that must share the Store identity, the developer copies the public key from
the item's Package page and passes it as `VYN_EXTENSION_PUBLIC_KEY` to `build:dev`. The build adds it
only to ignored `dist-dev/`. The upload ZIP can therefore never inherit a development key.

## Bootstrap sequence

The first upload has a deliberate two-step handshake:

1. Upload the keyless ZIP and copy the assigned Item ID.
2. Put that ID in the deployment environment and recreate the API before testing pairing.

An extension installed between these steps can render its controls, but pairing is expected to fail
because the API still allowlists the previous origin. Deferred publishing keeps that interval away
from users.

## Consequences

- Web Store signing and automatic updates replace the private CRX key as the production identity
  authority.
- A new Store item has a new ID. Changing `CHROME_EXTENSION_ID` invalidates the old extension origin
  and requires existing browsers to pair the Store build.
- Store releases must increment `manifest.version`.
- The public privacy policy, permission explanations, and reviewer instructions are release inputs,
  not optional marketing work.
