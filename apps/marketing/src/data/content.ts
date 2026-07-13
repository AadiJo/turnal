// Marketing copy kept in one place. All claims map to real `turnal` behavior
// in this repo.

export const product = {
  name: 'Turnal',
  tagline: 'A flight recorder for your AI coding agent.',
  sub: 'Turnal records every run your agent takes — the prompt, the tool calls, the files it touched — as hidden Git checkpoints beside your repo. Understand what happened, search it months later, and roll back any run safely. Local-first. Zero config.',
  install: 'npm install -g @aadijo/turnal',
  init: 'turnal init',
  agents: ['Claude Code', 'Codex'],
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
    body: 'A pre- and post-checkpoint on every agent run, committed to a hidden Git repo. Your real history and working tree stay untouched.',
    demo: 'log',
  },
  {
    k: 'read',
    title: 'Readable transcripts',
    body: 'Replay any session as a transcript — the prompt, the agent’s reply, and each tool call in order. No log spelunking.',
    demo: 'transcript',
  },
  {
    k: 'blame',
    title: 'Prompt-aware blame',
    body: 'Line-level blame that shows the prompt behind a change, not just a commit hash. Finally answer “why is this line here?”',
    demo: 'blame',
  },
  {
    k: 'search',
    title: 'Search every session',
    body: 'Full-text search across every prompt, reply, and tool call your agents have ever run — scoped to a session or across all of them.',
    demo: 'search',
  },
  {
    k: 'rollback',
    title: 'Safe rollback',
    body: 'Restore the workspace to any checkpoint. Turnal snapshots your current state first, so an undo is never destructive.',
    demo: 'rollback',
  },
  {
    k: 'local',
    title: 'Local-first',
    body: 'Recording data lives in .turnal/ on your machine — append-only events, a hidden Git store, and a disposable SQLite index. Turnal does not upload it.',
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
    body: 'One npm install, then turnal init wires hooks into Claude Code and Codex and creates the hidden store.',
  },
  {
    n: '02',
    title: 'Code as usual',
    body: 'Your agent works normally. Turnal captures each run in the background — prompt, tools, and file changes.',
  },
  {
    n: '03',
    title: 'Recall & recover',
    body: 'Later, log, blame, search, or diff any run — and roll back safely if a change went sideways.',
  },
];
