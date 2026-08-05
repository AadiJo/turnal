// Section groups for the Markdown documentation page. Keep IDs aligned with
// the heading slugs generated from src/content/docs/index.md.

export interface DocsNavItem {
  id: string;
  label: string;
}

export interface DocsNavGroup {
  label: string;
  items: DocsNavItem[];
}

export const docsNav: DocsNavGroup[] = [
  {
    label: 'Start',
    items: [
      { id: 'overview', label: 'Overview' },
      { id: 'installation', label: 'Installation' },
      { id: 'quickstart', label: 'Quickstart' },
    ],
  },
  {
    label: 'Understand',
    items: [
      { id: 'mental-model', label: 'Mental model' },
      { id: 'capture-boundaries', label: 'Capture boundaries' },
      { id: 'agent-integrations', label: 'Agent integrations' },
    ],
  },
  {
    label: 'Inspect',
    items: [
      { id: 'history-and-turns', label: 'History and turns' },
      { id: 'turnal-ui', label: 'Turnal UI' },
      { id: 'search', label: 'Search' },
      { id: 'intent-aware-blame', label: 'Intent-aware blame' },
    ],
  },
  {
    label: 'Recover',
    items: [
      { id: 'rollback', label: 'Rollback' },
      { id: 'replay-worktrees', label: 'Replay worktrees' },
    ],
  },
  {
    label: 'Evidence',
    items: [
      { id: 'verification', label: 'Verification' },
      { id: 'reproducibility-and-cases', label: 'Reproducibility and cases' },
      { id: 'vs-code', label: 'VS Code' },
    ],
  },
  {
    label: 'Operate',
    items: [
      { id: 'git-worktrees', label: 'Git worktrees' },
      { id: 'merge-stores', label: 'Merge stores' },
      { id: 'configuration', label: 'Configuration' },
      { id: 'privacy-and-storage', label: 'Privacy and storage' },
      { id: 'retention-and-removal', label: 'Retention' },
    ],
  },
  {
    label: 'Reference',
    items: [
      { id: 'command-reference', label: 'Command reference' },
      { id: 'troubleshooting', label: 'Troubleshooting' },
    ],
  },
];
