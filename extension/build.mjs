// Builds the exact Chrome Web Store payload. The app origin is compiled in
// from the manifest's single host permission so the two cannot drift.
import { build } from "esbuild";
import {
  cpSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

const here = path.dirname(fileURLToPath(import.meta.url));
const development = process.argv.includes("--development");
const dist = path.join(here, development ? "dist-dev" : "dist");
const release = path.join(here, "release");
const manifest = JSON.parse(
  readFileSync(path.join(here, "manifest.json"), "utf8"),
);

if (development) {
  const publicKey = process.env.VYN_EXTENSION_PUBLIC_KEY?.trim();
  if (!publicKey) {
    throw new Error(
      "VYN_EXTENSION_PUBLIC_KEY is required for a stable development build",
    );
  }
  manifest.key = publicKey;
}

function assertReleaseManifest() {
  const forbidden = ["key", "update_url"];
  for (const field of forbidden) {
    if (field in manifest) {
      throw new Error(
        `manifest.${field} is not allowed in a Chrome Web Store upload`,
      );
    }
  }
  if (manifest.manifest_version !== 3) {
    throw new Error("Chrome Web Store releases must use Manifest V3");
  }
  if (!/^\d+(?:\.\d+){0,3}$/.test(manifest.version)) {
    throw new Error(`invalid Chrome extension version: ${manifest.version}`);
  }
  if (!manifest.description || manifest.description.length > 132) {
    throw new Error("manifest.description must contain at most 132 characters");
  }
  if (manifest.permissions?.some((permission) => permission !== "storage")) {
    throw new Error("the release requests an unexpected extension permission");
  }
  if (
    manifest.host_permissions?.length !== 1 ||
    !manifest.host_permissions[0].startsWith("https://")
  ) {
    throw new Error(
      "the release must have exactly one secure API host permission",
    );
  }
}

if (!development) assertReleaseManifest();
const hostPermission = manifest.host_permissions[0];
const appOrigin = hostPermission.replace(/\/\*$/, "");

rmSync(dist, { recursive: true, force: true });
mkdirSync(dist, { recursive: true });

await build({
  entryPoints: {
    "service-worker": path.join(here, "src/service-worker.ts"),
    "content-script": path.join(here, "src/content-script.ts"),
    popup: path.join(here, "src/popup.ts"),
  },
  bundle: true,
  format: "esm",
  target: "es2022",
  outdir: dist,
  define: { __APP_ORIGIN__: JSON.stringify(appOrigin) },
  legalComments: "none",
  sourcemap: false,
  logLevel: "warning",
});

writeFileSync(
  path.join(dist, "manifest.json"),
  JSON.stringify(manifest, null, 2) + "\n",
);
cpSync(path.join(here, "src/popup.html"), path.join(dist, "popup.html"));
cpSync(path.join(here, "src/popup.css"), path.join(dist, "popup.css"));
cpSync(
  path.join(here, "src/content-script.css"),
  path.join(dist, "content-script.css"),
);
cpSync(path.join(here, "icons"), path.join(dist, "icons"), { recursive: true });

if (development) {
  console.log(
    `built ${dist} for local testing (${manifest.name} ${manifest.version})`,
  );
  process.exit(0);
}

// The versioned zip is the only file uploaded to the Web Store. Its manifest
// is at the archive root, as required by the Developer Dashboard.
mkdirSync(release, { recursive: true });
const zip = path.join(release, `visual-notes-${manifest.version}.zip`);
rmSync(zip, { force: true });
if (process.platform === "win32") {
  execFileSync("tar.exe", ["-a", "-c", "-f", zip, ...readdirSync(dist)], {
    cwd: dist,
    stdio: "inherit",
  });
} else {
  execFileSync("zip", ["-qr", zip, "."], { cwd: dist, stdio: "inherit" });
}
if (statSync(zip).size === 0) throw new Error("release archive is empty");
const archiveEntries = execFileSync(
  process.platform === "win32" ? "tar.exe" : "unzip",
  process.platform === "win32" ? ["-tf", zip] : ["-Z1", zip],
  { encoding: "utf8" },
)
  .split(/\r?\n/)
  .filter(Boolean);
if (!archiveEntries.includes("manifest.json")) {
  throw new Error("release archive does not contain manifest.json at its root");
}
console.log(
  `built ${zip} for ${appOrigin} (extension ${manifest.name} ${manifest.version})`,
);
