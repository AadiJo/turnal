// Marketing copy kept in one place. All claims map to real `turnal` behavior
// in this repo.

export const product = {
  name: 'Turnal',
  tagline: 'A local flight recorder for AI coding agents.',
  sub: 'Turnal pairs supported coding-agent activity with local events and hidden Git checkpoints. Search what happened, trace a line to the agent’s stated intent and human request, verify an earlier state, and roll back with a safety checkpoint first.',
  install: 'npm install -g @aadijo/turnal',
  init: 'turnal init',
  agents: ['Claude Code', 'Codex', 'Cursor', 'Pi', 'OpenCode', 'GitHub Copilot CLI'],
  repo: 'github.com/AadiJo/turnal',
};

export interface Feature {
  k: string;   // short key / number label
  title: string;
  body: string;
  demo: string; // demo id in demos.ts
}

export const features: Feature[] = [
  {
    k: 'record',
    title: 'Automatic checkpoints',
    body: 'A hidden pre- and post-checkpoint for every completed agent turn. Normal capture stays separate from your project Git history.',
    demo: 'log',
  },
  {
    k: 'read',
    title: 'Readable transcripts',
    body: 'Read exposed prompts, responses, and tool activity in turn order, with explicit redaction markers when your storage policy removes content.',
    demo: 'transcript',
  },
  {
    k: 'blame',
    title: 'Intent-aware blame',
    body: 'Line-level blame that keeps the agent’s stated problem separate from the human request, with honest labels when intent is missing, late, or outside scope.',
    demo: 'blame',
  },
  {
    k: 'search',
    title: 'Search every session',
    body: 'Full-text search across indexed prompts, replies, tools, paths, and events, scoped to one session or across attached worktrees.',
    demo: 'search',
  },
  {
    k: 'rollback',
    title: 'Safe rollback',
    body: 'Preview a restore, then return to a checkpoint after Turnal saves the current captured workspace as a safety point.',
    demo: 'rollback',
  },
  {
    k: 'local',
    title: 'Local-first',
    body: 'Turnal data lives in .turnal/ on your machine: append-only events, a hidden Git store, and a disposable SQLite index. Turnal does not upload it.',
    demo: 'diff',
  },
];

export interface Step {
  n: string;
  title: string;
  body: string;
}

export const steps: Step[] = [
  {
    n: '01',
    title: 'Install & init',
    body: 'One npm install, then turnal init wires supported agent integrations and creates the hidden store.',
  },
  {
    n: '02',
    title: 'Code as usual',
    body: 'Your agent works normally. Turnal captures each turn in the background, including the context exposed by its hooks and the resulting file changes.',
  },
  {
    n: '03',
    title: 'Recall & recover',
    body: 'Later, search, diff, blame, verify, or replay a saved state. Preview a rollback when you need to recover.',
  },
];
