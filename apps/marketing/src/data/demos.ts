/*
 * Representative Turnal CLI output for a mixed Claude Code + Codex workday.
 * Command shapes, labels, graph glyphs, and diff formatting follow the real
 * CLI. Session names, timestamps, and prompts form one coherent fixture so
 * every aggregate view exercises both supported adapters.
 *
 * Token classes map 1:1 to the CLI's ANSI colors:
 *   t-b   38;5;111  periwinkle: labels, session ids, tool names
 *   t-g   38;5;220  gold: counts and values
 *   t-ok  38;5;48   green: complete / post / additions
 *   t-gr  38;5;120  light green: session tags and graph rails
 *   t-del 38;5;203  red: deletions
 *   t-dim 2m        dim: secondary metadata
 */

export interface Demo {
  id: string;
  cmd: string;
  caption: string;
  out: string;
}

const b = (s: string) => `<span class="t-b">${s}</span>`;
const g = (s: string) => `<span class="t-g">${s}</span>`;
const ok = (s: string) => `<span class="t-ok">${s}</span>`;
const gr = (s: string) => `<span class="t-gr">${s}</span>`;
const del = (s: string) => `<span class="t-del">${s}</span>`;
const dim = (s: string) => `<span class="t-dim">${s}</span>`;

export const demos: Record<string, Demo> = {
  init: {
    id: 'init',
    cmd: 'turnal init',
    caption: 'One command connects both supported agents and starts recording.',
    out: [
      `initialized hidden git repo: ${dim('.turnal/git')}`,
      `worktree id: ${dim('wt_1d994d3024c8')}`,
      `updated gitignore: ${dim('.gitignore')}`,
      `configured claude hooks: ${dim('.claude/settings.json')}`,
      `configured codex hooks: ${dim('.codex/config.toml')}`,
    ].join('\n'),
  },

  log: {
    id: 'log',
    cmd: 'turnal log',
    caption: 'One timeline across every Claude Code and Codex session in the repo.',
    out: [
      ``,
      `checkpoint graph: 3 sessions, 6 turns, 3 lanes`,
      ``,
      `sessions: ${gr('[codex 15:08]')} ${gr('[claude 15:02]')} ${gr('[codex 14:48]')}`,
      ``,
      `${gr('* | | ')}${dim('4b93a11c2e70')} - 15:12 ${gr('[codex 15:08]')} turn 2  ${b('Edit +1')} ${dim('complete')} ${dim('2 files +14 -7; codex; Edit, Bash')}`,
      `${gr('| | | ')}${dim('Prompt:')} "The Stripe webhook can be delivered twice. Claim the event id in Postgres before dispatch and make duplicate delivery a no-op."`,
      `${gr('| | |')}`,
      `${gr('| * | ')}${dim('a70fc6214bd9')} - 15:06 ${gr('[claude 15:02]')} turn 2  ${b('Edit +1')} ${dim('complete')} ${dim('1 file +9 -3; claude-code; Edit, Bash')}`,
      `${gr('| | | ')}${dim('Prompt:')} "Signature checks fail in production because Express parsed the Stripe body first. Preserve raw bytes only on /webhooks/stripe."`,
      `${gr('| | |')}`,
      `${gr('* | | ')}${dim('e2948e3df10a')} - 15:02 ${gr('[codex 15:08]')} turn 1  ${b('Write +1')} ${dim('complete')} ${dim('1 file +34 -0; codex; Write, Bash')}`,
      `${gr('| | | ')}${dim('Prompt:')} "Add a concurrency test that sends the same invoice.paid event eight times and proves fulfillment runs once."`,
      `${gr('| | |')}`,
      `${gr('| * | ')}${dim('1cf588b4d7a1')} - 14:57 ${gr('[claude 15:02]')} turn 1  ${b('Read +1')} ${dim('complete')} ${dim('2 files +11 -5; claude-code; Read, Edit')}`,
      `${gr('| | | ')}${dim('Prompt:')} "Trace why webhook retries return 500 after the order was already fulfilled. Keep 5xx only for genuinely retryable failures."`,
      `${gr('| | |')}`,
      `${gr('| | * ')}${dim('6d8f02ccff41')} - 14:52 ${gr('[codex 14:48]')} turn 2  ${b('Edit +1')} ${dim('complete')} ${dim('3 files +22 -8; codex; Edit, Bash')}`,
      `${gr('| | | ')}${dim('Prompt:')} "Move payment-provider timeouts into config, validate them at startup, and keep local test defaults unchanged."`,
      `${gr('| | |')}`,
      `${gr('| | * ')}${dim('05aa319b8c64')} - 14:48 ${gr('[codex 14:48]')} turn 1  ${b('Write +1')} ${dim('complete')} ${dim('2 files +41 -2; codex; Read, Write')}`,
      `${gr('      ')}${dim('Prompt:')} "Instrument checkout confirmation with request id, Stripe event id, and order id so on-call can correlate a failed charge."`,
    ].join('\n'),
  },

  transcript: {
    id: 'transcript',
    cmd: 'turnal log --transcript',
    caption: 'Replay Claude Code and Codex turns with prompts, replies, and tools intact.',
    out: [
      ``,
      `transcript log: 2 sessions, 4 turns`,
      ``,
      `Session: ${gr('[codex 15:08]')}`,
      `* ${dim('4b93a11c2e70')} - ${b('Edit +1')} - 15:12 - turn 2`,
      `  src/webhooks/stripe.ts ${ok('+10')} ${del('-5')}`,
      `  test/webhooks/stripe.test.ts ${ok('+4')} ${del('-2')}`,
      ``,
      `  Human: The Stripe webhook can be delivered twice. Claim the event id in Postgres before`,
      `         dispatch and make duplicate delivery a no-op.`,
      `    ↓`,
      `  Agent: Added an insert-on-conflict claim before dispatch. Duplicate events now return 200`,
      `         without running fulfillment again; the concurrency test passes.`,
      `      ├─ ${ok('Edit')} (file_path: src/webhooks/stripe.ts)`,
      `      └─ ${b('Bash')} (command: pnpm test stripe --runInBand)`,
      ``,
      `Session: ${gr('[claude 15:02]')}`,
      `* ${dim('a70fc6214bd9')} - ${b('Edit +1')} - 15:06 - turn 2`,
      `  src/http/app.ts ${ok('+9')} ${del('-3')}`,
      ``,
      `  Human: Signature checks fail in production because Express parsed the Stripe body first.`,
      `         Preserve the raw bytes only on /webhooks/stripe.`,
      `    ↓`,
      `  Agent: Scoped express.raw to the Stripe route and left JSON parsing unchanged elsewhere.`,
      `         Signature verification and API integration tests both pass.`,
      `      ├─ ${g('Edit')} (file_path: src/http/app.ts)`,
      `      └─ ${b('Bash')} (command: pnpm test webhook api)`,
    ].join('\n'),
  },

  blame: {
    id: 'blame',
    cmd: 'turnal blame src/webhooks/stripe.ts',
    caption: 'Mixed-agent blame keeps each action’s stated intent separate from the human request.',
    out: [
      `14:57 ${gr('[claude 15:02]')} turn 1  ${dim('21')} | export async function receiveStripe(req, res) {`,
      `  ${dim('Intent: fulfilled webhook replays were treated as retryable failures')}`,
      `  ${dim('Human request: "Trace why webhook retries return 500 after fulfillment..."')}`,
      `15:06 ${gr('[claude 15:02]')} turn 2  ${dim('22')} |   const event = verify(req.rawBody, signature)`,
      `  ${dim('Intent: signature verification received parsed rather than raw request bytes')}`,
      `  ${dim('Human request: "Signature checks fail after Express parses the Stripe body..."')}`,
      `15:12 ${gr('[codex 15:08]')} turn 2   ${dim('23')} |   const claimed = await claims.insert(event.id)`,
      `  ${dim('Intent: duplicate deliveries could dispatch fulfillment more than once')}`,
      `  ${dim('Human request: "Claim duplicate Stripe event ids before dispatch..."')}`,
      `15:12 ${gr('[codex 15:08]')} turn 2   ${dim('24')} |   if (!claimed) return res.sendStatus(200)`,
      `  ${dim('Intent: duplicate deliveries could dispatch fulfillment more than once')}`,
      `  ${dim('Human request: "Claim duplicate Stripe event ids before dispatch..."')}`,
      `14:57 ${gr('[claude 15:02]')} turn 1  ${dim('25')} |   await dispatch(event)`,
      `  ${dim('Intent: fulfilled webhook replays were treated as retryable failures')}`,
      `  ${dim('Human request: "Trace why webhook retries return 500 after fulfillment..."')}`,
      `15:12 ${gr('[codex 15:08]')} turn 2   ${dim('26')} |   return res.sendStatus(200)`,
      `  ${dim('Intent: duplicate deliveries could dispatch fulfillment more than once')}`,
      `  ${dim('Human request: "Claim duplicate Stripe event ids before dispatch..."')}`,
    ].join('\n'),
  },

  search: {
    id: 'search',
    cmd: 'turnal search "invoice.paid"',
    caption: 'Search prompts, replies, and tools across every agent session.',
    out: [
      `codex-b91e:1  ${dim('worktree=wt_1d994d3024c8')}  codex  ${dim('2026-07-09 15:02:18 UTC')}`,
      `  prompt: Add a concurrency test that sends the same invoice.paid event eight times...`,
      `  assistant: Added a barrier-synchronized test; fulfillment runs once across all eight deliveries.`,
      `  tools: Write, Bash`,
      `  files: test/webhooks/stripe.test.ts`,
      `  match: ... sends the same ${g('[invoice.paid]')} event eight times ...`,
      ``,
      `claude-7f2a:1  ${dim('worktree=wt_1d994d3024c8')}  claude-code  ${dim('2026-07-09 14:57:44 UTC')}`,
      `  prompt: Trace why webhook retries return 500 after the order was already fulfilled...`,
      `  assistant: Found invoice.paid falling through after fulfillment; successful replays now return 200.`,
      `  tools: Read, Edit, Bash`,
      `  files: src/webhooks/stripe.ts, test/webhooks/retry.test.ts`,
      `  match: ... ${g('[invoice.paid]')} replay reaches the completed-order branch ...`,
    ].join('\n'),
  },

  diff: {
    id: 'diff',
    cmd: 'turnal diff codex-b91e:2',
    caption: 'The exact Codex change, read straight from its hidden checkpoint.',
    out: [
      `${b('diff --git a/src/webhooks/stripe.ts b/src/webhooks/stripe.ts')}`,
      `${dim('index 8c571a4..d93a20e 100644')}`,
      `${dim('--- a/src/webhooks/stripe.ts')}`,
      `${dim('+++ b/src/webhooks/stripe.ts')}`,
      `${b('@@ -20,7 +20,12 @@ export async function receiveStripe(req, res) {')}`,
      `   const event = verify(req.rawBody, signature)`,
      `${del('-  await dispatch(event)')}`,
      `${ok('+  const claimed = await claims.insert(event.id)')}`,
      `${ok('+  if (!claimed) return res.sendStatus(200)')}`,
      `${ok('+')}`,
      `${ok('+  try {')}`,
      `${ok('+    await dispatch(event)')}`,
      `${ok('+  } catch (error) {')}`,
      `${ok('+    await claims.release(event.id)')}`,
      `${ok('+    throw error')}`,
      `${ok('+  }')}`,
    ].join('\n'),
  },

  rollback: {
    id: 'rollback',
    cmd: 'turnal rollback --to claude-7f2a:1:pre',
    caption: 'Undo a Claude Code turn after snapshotting the current mixed-agent workspace.',
    out: [
      `Rollback complete`,
      `  target: claude-7f2a:turn:1:pre`,
      `  id:     ${dim('1cf588b4d7a1')}`,
      `  ref:    ${dim('refs/agent-vcs/checkpoints/claude-7f2a/turn/000001/pre')}`,
      ``,
      `Previous workspace saved`,
      `  id:  ${dim('80f4f334992a')}`,
      `  ref: ${dim('refs/agent-vcs/rollback-safety/claude-7f2a/turn/000001/pre/1783629131200000000-a4c9e2f18b7d')}`,
    ].join('\n'),
  },

  sessions: {
    id: 'sessions',
    cmd: 'turnal sessions',
    caption: 'Claude Code and Codex sessions, side by side.',
    out: [
      `${b('sessions')} ${g('2')} ${dim('recorded')}`,
      ``,
      `${ok('[COMPLETE]')} ${b('codex-b91e')}`,
      `  ${b('adapter ')} ${b('codex')} ${dim('/ workspace-write')}`,
      `  ${b('turns   ')} ${g('2')} total, ${g('2')} ${ok('complete')}`,
      `  ${b('events  ')} ${g('24')}`,
      `  ${b('activity')} ${dim('2026-07-09 15:02:18 UTC -> 2026-07-09 15:12:07 UTC')}`,
      `  ${b('head    ')} turn 2 ${ok('post')} ${dim('4b93a11c2e70')}`,
      `  ${b('prompt  ')} ${g('"The Stripe webhook can be delivered twice. Claim the event id..."')}`,
      `  ${b('tools   ')} ${b('Edit, Bash')}`,
      ``,
      `${ok('[COMPLETE]')} ${b('claude-7f2a')}`,
      `  ${b('adapter ')} ${b('claude-code')}`,
      `  ${b('turns   ')} ${g('2')} total, ${g('2')} ${ok('complete')}`,
      `  ${b('events  ')} ${g('21')}`,
      `  ${b('activity')} ${dim('2026-07-09 14:57:44 UTC -> 2026-07-09 15:06:31 UTC')}`,
      `  ${b('head    ')} turn 2 ${ok('post')} ${dim('a70fc6214bd9')}`,
      `  ${b('prompt  ')} ${g('"Signature checks fail in production because Express parsed..."')}`,
      `  ${b('tools   ')} ${b('Edit, Bash')}`,
    ].join('\n'),
  },
};

export const install = 'npm install -g @aadijo/turnal';
