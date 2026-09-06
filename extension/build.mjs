// Builds dist/: three esbuild bundles (ES2022, unminified), the manifest,
// the popup, the stylesheet and the icons, then dist.zip. The app origin is
// compiled in from the manifest's host permission so the two cannot drift.
import { build } from "esbuild";
import {
  cpSync,
  mkdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { execFileSync } from "node:child_process";

const here = path.dirname(fileURLToPath(import.meta.url));
const dist = path.join(here, "dist");
const manifest = JSON.parse(
  readFileSync(path.join(here, "manifest.json"), "utf8"),
);
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
  logLevel: "warning",
});

writeFileSync(
  path.join(dist, "manifest.json"),
  JSON.stringify(manifest, null, 2) + "\n",
);
cpSync(path.join(here, "src/popup.html"), path.join(dist, "popup.html"));
cpSync(
  path.join(here, "src/content-script.css"),
  path.join(dist, "content-script.css"),
);
cpSync(path.join(here, "icons"), path.join(dist, "icons"), { recursive: true });

// The zip is what the release page carries; `zip` on the runner, PowerShell
// on a Windows developer machine.
const zip = path.join(here, "dist.zip");
rmSync(zip, { force: true });
try {
  if (process.platform === "win32") {
    execFileSync(
      "powershell",
      [
        "-NoProfile",
        "-Command",
        `Compress-Archive -Path '${dist}\\*' -DestinationPath '${zip}'`,
      ],
      { stdio: "inherit" },
    );
  } else {
    execFileSync("zip", ["-qr", zip, "."], { cwd: dist, stdio: "inherit" });
  }
} catch (err) {
  console.warn("dist.zip not written:", err.message);
}
console.log(
  `built ${dist} for ${appOrigin} (extension ${manifest.name} ${manifest.version})`,
);
