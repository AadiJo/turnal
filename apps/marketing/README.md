# Turnal — marketing site

An [Astro](https://astro.build) static site for Turnal. The landing page lives at
`/`; production-facing product documentation lives at `/docs`.

```sh
cd apps/marketing
npm install
npm run dev      # http://localhost:4321
npm run build    # -> dist/
npm run preview
```

The direction is deliberately singular: a dark, terminal-forward page built
around the flight-recorder story, with international orange used as a restrained
accent. Do not add alternate option pages back into the site.

## The terminal demos are command-faithful

The demos use the real CLI's commands, labels, graph glyphs, and output shapes.
Their session names, timestamps, prompts, and diffs form a representative
Claude Code + Codex fixture, so aggregate commands demonstrate a mixed-agent
workday instead of repeating one session.

`src/data/demos.ts` holds the output as tokenized HTML. The token classes map
1:1 to the ANSI colors the CLI emits (periwinkle labels, gold counts, green
completions, red deletions, dim metadata). Check new fixtures against the current
CLI before changing them; do not invent adapters, commands, or flags.

## Design rules

- Use warm black (`#100f0d`) and cream (`#ece3d4`) as the base. International
  orange (`#ff4f00`) is the single brand accent and should mark state, not fill
  large surfaces.
- Inter carries product copy; JetBrains Mono is for commands, paths, metadata,
  and terminal output. Avoid wide, uppercase marketing kickers.
- Keep documentation prose near 800px wide and use the sidebar rail for active
  section state. Section movement uses a short 300ms ease-in-out; the active rail
  segment and title color should respond without layout work during scrolling.
- Code and terminal content wraps. Product examples must never require an
  internal horizontal or vertical scroll area to reveal their content.
- Use hairline borders and spacing for hierarchy. Motion should be short,
  functional, and disabled by `prefers-reduced-motion`.

The Claude and Codex marks in `public/brands/` are cached from the
[SVGL registry](https://svgl.app/) so the page does not hot-link third-party
assets at render time. Keep each asset's source comment when updating it.

## Structure

```
src/
  data/demos.ts       command-faithful multi-agent fixtures, tokenized
  data/content.ts     marketing copy (single source of truth)
  components/
    Terminal.astro    window chrome + ANSI-colored output; themeable via CSS vars
    Logo.astro        inline mark (rollback loop + checkpoint beads + REC dot)
    CopyButton.astro  icon copy control with brief checkmark feedback
    CodeBlock.astro   wrapping, copyable documentation examples
    DocsNav.astro     responsive documentation navigation
  layouts/Base.astro  head, fonts, favicon
  pages/              landing page + product documentation
```
