import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const extensionRoot = path.resolve(__dirname, "../..");

test("keeps the first Marketplace release at 0.0.1", async () => {
  const manifest = JSON.parse(
    await readFile(path.join(extensionRoot, "package.json"), "utf8"),
  ) as { version?: unknown; icon?: unknown };

  assert.equal(manifest.version, "0.0.1");
  assert.equal(manifest.icon, "media/icon.png");
});

test("ships Marketplace artwork at usable dimensions", async () => {
  const readme = await readFile(path.join(extensionRoot, "README.md"), "utf8");
  const assets = [
    { relativePath: "media/icon.png", minimumWidth: 128, minimumHeight: 128 },
    { relativePath: "media/screenshots/inline-blame-hover.png", minimumWidth: 1000, minimumHeight: 600 },
    { relativePath: "media/screenshots/turn-diff-native.png", minimumWidth: 1000, minimumHeight: 600 },
    { relativePath: "media/screenshots/rollback-preview.png", minimumWidth: 1000, minimumHeight: 600 },
  ];

  for (const asset of assets) {
    const { width, height } = pngDimensions(
      await readFile(path.join(extensionRoot, asset.relativePath)),
    );
    assert.ok(width >= asset.minimumWidth, `${asset.relativePath} is only ${width}px wide`);
    assert.ok(height >= asset.minimumHeight, `${asset.relativePath} is only ${height}px tall`);
    if (asset.relativePath.includes("screenshots/")) {
      assert.match(readme, new RegExp(escapeRegExp(asset.relativePath)));
    }
  }
});

function pngDimensions(content: Buffer): { width: number; height: number } {
  const signature = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  if (content.length < 24 || !content.subarray(0, 8).equals(signature)) {
    throw new TypeError("asset is not a PNG");
  }
  return { width: content.readUInt32BE(16), height: content.readUInt32BE(20) };
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
