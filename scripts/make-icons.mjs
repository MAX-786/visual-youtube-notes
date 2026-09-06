// Writes the PWA and extension icons as flat PNGs with no dependencies: a
// rounded navy square with a pale "V". Production artwork can replace them;
// the manifest and the extension only need the files to exist at these sizes.
import { deflateSync } from "node:zlib";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

const crcTable = new Uint32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
const crc32 = (buf) => {
  let c = 0xffffffff;
  for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
};
const chunk = (type, data) => {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
};

function png(size) {
  const navy = [0x1f, 0x3a, 0x5f];
  const pale = [0xf7, 0xf7, 0xf5];
  const radius = size * 0.2;
  const rows = [];
  for (let y = 0; y < size; y++) {
    const row = [0];
    for (let x = 0; x < size; x++) {
      // Rounded corners.
      const cx = Math.min(Math.max(x, radius), size - radius);
      const cy = Math.min(Math.max(y, radius), size - radius);
      const inside = (x - cx) ** 2 + (y - cy) ** 2 <= radius * radius;
      // A chunky V: two diagonal strokes meeting at the bottom centre.
      const u = x / size;
      const v = y / size;
      const stroke = 0.11;
      const left = Math.abs(u - (0.24 + v * 0.26)) < stroke / 2;
      const right = Math.abs(u - (0.76 - v * 0.26)) < stroke / 2;
      const letter = v > 0.28 && v < 0.78 && (left || right);
      const color = !inside
        ? [0, 0, 0, 0]
        : letter
          ? [...pale, 255]
          : [...navy, 255];
      row.push(...color);
    }
    rows.push(Buffer.from(row));
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(Buffer.concat(rows))),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

const targets = [
  ["web/static/icons", [192, 512]],
  ["extension/icons", [16, 48, 128]],
];
for (const [dir, sizes] of targets) {
  mkdirSync(dir, { recursive: true });
  for (const size of sizes) {
    writeFileSync(path.join(dir, `icon-${size}.png`), png(size));
  }
}
console.log("icons written");
