#!/usr/bin/env node
// make-ico.js — wrap a PNG into a single-image .ico container.
//
// Modern Windows (Vista+) renders PNG-compressed ICO entries natively, so no
// pixel re-encoding is needed; we only prepend the 6-byte ICONDIR header and
// one 16-byte ICONDIRENTRY. The PNG must be square and ≤256px (the build
// script resizes with sips before calling this).
//
// Usage: node make-ico.js <in.png> <out.ico>

const fs = require('fs');

const [src, dst] = process.argv.slice(2);
if (!src || !dst) {
  console.error('usage: make-ico.js <in.png> <out.ico>');
  process.exit(1);
}

const png = fs.readFileSync(src);
if (png.readUInt32BE(0) !== 0x89504e47) {
  console.error(`make-ico: ${src} is not a PNG`);
  process.exit(1);
}
const w = png.readUInt32BE(16);
const h = png.readUInt32BE(20);
if (w > 256 || h > 256) {
  console.error(`make-ico: ${w}x${h} exceeds 256px ICO limit — resize first`);
  process.exit(1);
}

const header = Buffer.alloc(6);
header.writeUInt16LE(0, 0); // reserved
header.writeUInt16LE(1, 2); // type: icon
header.writeUInt16LE(1, 4); // image count

const entry = Buffer.alloc(16);
entry[0] = w === 256 ? 0 : w; // 0 means 256
entry[1] = h === 256 ? 0 : h;
entry.writeUInt16LE(1, 4); // color planes
entry.writeUInt16LE(32, 6); // bits per pixel
entry.writeUInt32LE(png.length, 8); // image data size
entry.writeUInt32LE(22, 12); // data offset (6 + 16)

fs.writeFileSync(dst, Buffer.concat([header, entry, png]));
console.log(`make-ico: ${dst} (${w}x${h}, ${png.length} bytes PNG payload)`);
