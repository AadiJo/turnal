import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const extensionRoot = path.resolve(__dirname, "../..");

test("keeps the Marketplace update at 0.0.3", async () => {
  const manifest = JSON.parse(
    await readFile(path.join(extensionRoot, "package.json"), "utf8"),
  ) as {
    version?: unknown;
    publisher?: unknown;
    author?: { name?: unknown; url?: unknown };
    icon?: unknown;
    repository?: { url?: unknown };
    homepage?: unknown;
    bugs?: { url?: unknown };
  };

  assert.equal(manifest.version, "0.0.3");
  assert.equal(manifest.publisher, "aadijo");
  assert.deepEqual(manifest.author, {
    name: "Advait Johari",
    url: "https://github.com/aadijo",
  });
  assert.equal(manifest.icon, "media/icon.png");
  assert.equal(manifest.repository?.url, "https://github.com/aadijo/turnal");
  assert.equal(manifest.homepage, "https://github.com/aadijo/turnal#readme");
  assert.equal(manifest.bugs?.url, "https://github.com/aadijo/turnal/issues");
});

test("ships Marketplace artwork at usable dimensions", async () => {
  const readme = await readFile(path.join(extensionRoot, "README.md"), "utf8");
  const assets = [
    { relativePath: "media/icon.png", minimumWidth: 128, minimumHeight: 128 },
    {
      relativePath: "media/screenshots/inline-blame-hover.png",
      publicUrl: "https://i.imgur.com/eMnXYM9.png",
      minimumWidth: 1000,
      minimumHeight: 600,
    },
    {
      relativePath: "media/screenshots/turn-diff-native.png",
      publicUrl: "https://i.imgur.com/nHN4gNo.png",
      minimumWidth: 1000,
      minimumHeight: 600,
    },
    {
      relativePath: "media/screenshots/rollback-preview.png",
      publicUrl: "https://i.imgur.com/aOOe2gd.png",
      minimumWidth: 1000,
      minimumHeight: 600,
    },
  ];

  for (const asset of assets) {
    const content = await readFile(path.join(extensionRoot, asset.relativePath));
    const { width, height } = pngDimensions(content);
    assert.ok(width >= asset.minimumWidth, `${asset.relativePath} is only ${width}px wide`);
    assert.ok(height >= asset.minimumHeight, `${asset.relativePath} is only ${height}px tall`);
    if (asset.relativePath === "media/icon.png") {
      assert.equal(content[25], 6, "media/icon.png must be an RGBA PNG with transparency");
    }
    if (asset.relativePath.includes("screenshots/")) {
      assert.ok(asset.publicUrl, `${asset.relativePath} needs a public Marketplace URL`);
      assert.match(readme, new RegExp(escapeRegExp(asset.publicUrl)));
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
