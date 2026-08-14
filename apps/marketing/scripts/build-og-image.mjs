/**
 * Regenerates public/og-image.png, the social card served to Open Graph and
 * Twitter crawlers.
 *
 * The output is committed, so this only needs to run when the card design or
 * the brand mark changes: `node scripts/build-og-image.mjs`.
 *
 * Text is rasterized by the host's font stack, so Inter and JetBrains Mono
 * must be installed locally for the render to match the site. If they are
 * missing the script still succeeds but falls back to a default sans, which
 * is why the result is committed rather than generated during the build.
 */
import sharp from 'sharp';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '../../..');
const outPath = resolve(here, '../public/og-image.png');

const W = 1200;
const H = 630;
const BG = '#100f0d';
const FG = '#ece3d4';
const ACCENT = '#ff4f00';
const HAIR = 'rgba(236,227,212,0.11)';
const MUTED = 'rgba(236,227,212,0.58)';

// Reuse the real brand mark rather than a copy, so the card cannot drift from
// the logo. currentColor is resolved here because the SVG is inlined without
// an inheriting parent.
const markInner = readFileSync(resolve(repoRoot, 'assets/logo-mark.svg'), 'utf8')
  .replace(/^[\s\S]*?<svg[^>]*>/, '')
  .replace(/<\/svg>\s*$/, '')
  .replaceAll('currentColor', FG);

// Terminal sample echoing the homepage hero, kept short so it stays legible at
// the small sizes social cards actually render at.
const lines = [
  [['$ ', MUTED], ['turnal log', FG]],
  [['●  turn 4', FG], ['  fix auth redirect loop', MUTED]],
  [['●  turn 3', FG], ['  add session cookie tests', MUTED]],
  [['●  turn 2', FG], ['  refactor token refresh', MUTED]],
];

const termX = 700;
const termY = 232;
const termW = 420;
const lineH = 40;

// Advance x manually per span: SVG has no inline layout, and JetBrains Mono's
// advance width at 19px is a known constant.
const MONO_ADVANCE = 11.4;

const termLines = lines
  .map((parts, i) => {
    let x = termX + 28;
    return parts
      .map(([text, fill]) => {
        const y = termY + 68 + i * lineH;
        const el = `<text x="${x}" y="${y}" font-family="JetBrains Mono" font-size="19" fill="${fill}">${text.replace(/&/g, '&amp;')}</text>`;
        x += text.length * MONO_ADVANCE;
        return el;
      })
      .join('');
  })
  .join('');

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <rect width="${W}" height="${H}" fill="${BG}"/>

  <g transform="translate(80,74) scale(0.2265)">${markInner}</g>
  <text x="142" y="121" font-family="Inter" font-size="45" font-weight="700" letter-spacing="-2.9" fill="${FG}">turnal</text>

  <text x="80" y="286" font-family="Inter" font-size="70" font-weight="660" letter-spacing="-3.6" fill="${FG}">Every agent turn,</text>
  <text x="80" y="360" font-family="Inter" font-size="70" font-weight="660" letter-spacing="-3.6" fill="rgba(236,227,212,0.48)">on the record.</text>

  <text x="80" y="424" font-family="Inter" font-size="23" font-weight="400" fill="${MUTED}">Local history for AI coding agents.</text>
  <text x="80" y="459" font-family="Inter" font-size="23" font-weight="400" fill="${MUTED}">Search, diff, verify, and roll back safely.</text>

  <rect x="80" y="510" width="340" height="52" rx="11" fill="rgba(236,227,212,0.05)" stroke="${HAIR}"/>
  <text x="102" y="543" font-family="JetBrains Mono" font-size="19" fill="${FG}"><tspan fill="${ACCENT}">$</tspan> npm i -g @aadijo/turnal</text>

  <rect x="${termX}" y="${termY}" width="${termW}" height="${lineH * lines.length + 74}" rx="14" fill="#17130f" stroke="${HAIR}"/>
  <circle cx="${termX + 26}" cy="${termY + 26}" r="6" fill="rgba(236,227,212,0.22)"/>
  <circle cx="${termX + 46}" cy="${termY + 26}" r="6" fill="rgba(236,227,212,0.22)"/>
  <circle cx="${termX + 66}" cy="${termY + 26}" r="6" fill="rgba(236,227,212,0.22)"/>
  <line x1="${termX}" y1="${termY + 48}" x2="${termX + termW}" y2="${termY + 48}" stroke="${HAIR}"/>
  ${termLines}

  <rect x="0" y="${H - 7}" width="${W}" height="7" fill="${ACCENT}"/>
</svg>`;

// resize pins the output to exactly 1200x630 regardless of the rasterizer's
// assumed DPI; Twitter and Slack reject cards that miss the expected size.
await sharp(Buffer.from(svg), { density: 96 })
  .resize(W, H)
  .png({ quality: 90, compressionLevel: 9 })
  .toFile(outPath);

console.log(`wrote ${outPath}`);
