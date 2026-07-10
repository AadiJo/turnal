# Turnal — marketing site

An [Astro](https://astro.build) static site for Turnal. The landing page lives at
`/`; its lightweight design reference for future contributors lives at `/docs`.

```sh
cd web
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

## Structure

```
src/
  data/demos.ts       command-faithful multi-agent fixtures, tokenized
  data/content.ts     marketing copy (single source of truth)
  components/
    Terminal.astro    window chrome + ANSI-colored output; themeable via CSS vars
    Logo.astro        inline mark (rollback loop + checkpoint beads + REC dot)
    CopyButton.astro  icon copy control with brief checkmark feedback
  layouts/Base.astro  head, fonts, favicon
  pages/              landing page + design guidelines
```
