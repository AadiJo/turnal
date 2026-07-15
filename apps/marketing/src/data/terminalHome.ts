export interface CommandPanel {
  id: string;
  label: string;
  command: string;
  description: string;
  output: string;
}

export interface WorkflowScene {
  id: string;
  title: string;
  command: string;
  output: string[];
  durationMs: number;
  mode?: 'stream' | 'pager';
}

const lane = (...cells: Array<'*' | '|' | ''>) => cells
  .map((cell, index) => `<span class="th-lane-token th-session-${index % 6}">${cell ? `${cell} ` : '  '}</span>`)
  .join('');
const rollbackLane = (count: number) => `<span class="th-danger">${'! '.repeat(count)}</span>`;
const hash = (value: string) => `<span class="th-hash">${value}</span>`;
const session = (value: string, index: number) => `<span class="th-session-${index % 6}">${value}</span>`;
const action = (value: string) => `<span class="th-action">${value}</span>`;
const muted = (value: string) => `<span class="th-muted">${value}</span>`;
const success = (value: string) => `<span class="th-success">${value}</span>`;
const warning = (value: string) => `<span class="th-warning">${value}</span>`;
const danger = (value: string) => `<span class="th-danger">${value}</span>`;
const label = (value: string) => `<span class="th-label">${value}</span>`;

export const installCommand = 'npm install -g @aadijo/turnal';

export const graphLines = [
  '',
  `${label('checkpoint graph:')} 5 sessions, 13 turns, 2 rollbacks`,
  '',
  `${label('sessions:')} ${session('[claude 14:20]', 0)} ${session('[claude 14:46]', 1)} ${session('[claude 14:16]', 2)} ${session('[codex 14:28]', 3)} ${session('[codex 14:12]', 4)}`,
  '',
  `${lane('*', '', '', '', '')}${hash('59a91d6d715f')} - 14:58 ${session('[claude 14:20]', 0)} turn 4      ${action('Read +2')} ${muted('complete')} ${muted('2 files +18 -6')}  ${muted('24 events; claude-code; tools: Read, Edit, Bash')}`,
  `${lane('|', '', '', '', '')}${muted('Prompt:')} "Return success for an event another worker already completed."`,
  `${lane('|', '', '', '', '')}`,
  `${lane('|', '*', '', '', '')}${hash('583cb027b9f3')} - 14:56 ${session('[claude 14:46]', 1)} turn 2      ${action('Read +2')} ${muted('complete')} ${muted('3 files +31 -14')}  ${muted('22 events; claude-code; tools: Read, Edit, Bash')}`,
  `${lane('|', '|', '', '', '')}${muted('Prompt:')} "Attach order and event ids to every checkout error."`,
  `${lane('|', '|', '', '', '')}`,
  `${lane('|', '|', '', '*', '')}${hash('e853a4e34acc')} - 14:54 ${session('[codex 14:28]', 3)} turn 2      ${action('Read +2')} ${muted('complete')} ${muted('2 files +24 -3')}  ${muted('19 events; codex; tools: Read, Edit, Bash')}`,
  `${lane('|', '|', '', '|', '')}${muted('Prompt:')} "Validate provider timeout values during startup."`,
  `${lane('|', '|', '', '|', '')}`,
  `${rollbackLane(5)}${danger('------------')} ${danger('reverted to')} ${session('[codex 14:12]', 4)} turn 2 ${hash('68459d51926d')}`,
  `${lane('|', '|', '|', '|', '|')}`,
  `${lane('*', '|', '', '|', '')}${hash('5027e8e66e1f')} - 14:49 ${session('[claude 14:20]', 0)} turn 3      ${action('Read +2')} ${muted('complete')} ${muted('2 files +42 -19')}  ${muted('28 events; claude-code; tools: Read, Edit, Bash')}`,
  `${lane('|', '|', '', '|', '')}${muted('Prompt:')} "Move the idempotency claim into the fulfillment transaction."`,
  `${lane('|', '|', '', '|', '')}`,
  `${lane('|', '*', '', '|', '')}${hash('7d073c54cb16')} - 14:46 ${session('[claude 14:46]', 1)} turn 1      ${action('Read +3')} ${muted('complete')} ${muted('3 files +57 -8')}  ${muted('26 events; claude-code; tools: Read, Write, Edit, Bash')}`,
  `${lane('|', '|', '', '|', '')}${muted('Prompt:')} "Add payment lifecycle correlation ids."`,
  `${lane('|', '|', '', '|', '')}`,
  `${lane('|', '', '', '|', '*')}${hash('3251ca08a1b4')} - 14:43 ${session('[codex 14:12]', 4)} turn 3      ${action('Read +2')} ${muted('complete')} ${muted('2 files +29 -12')}  ${muted('21 events; codex; tools: Read, Edit, Bash')}`,
  `${lane('|', '', '', '|', '|')}${muted('Prompt:')} "Separate duplicate delivery metrics from provider retries."`,
  `${lane('|', '', '', '|', '|')}`,
  `${lane('|', '', '*', '|', '|')}${hash('7f68c3762e1a')} - 14:40 ${session('[claude 14:16]', 2)} turn 2      ${action('Read +2')} ${muted('complete')} ${muted('2 files +16 -10')}  ${muted('18 events; claude-code; tools: Read, Edit, Bash')}`,
  `${lane('|', '', '|', '|', '|')}${muted('Prompt:')} "Scope raw-body parsing to the Stripe route."`,
  `${lane('|', '', '|', '|', '|')}`,
  `${rollbackLane(5)}${danger('------------')} ${danger('reverted to')} ${session('[claude 14:20]', 0)} turn 1 ${action('pre')} ${hash('614611d0aa9f')}`,
  `${lane('|', '|', '|', '|', '|')}`,
  `${lane('*', '', '|', '|', '|')}${hash('eadff0409763')} - 14:32 ${session('[claude 14:20]', 0)} turn 2      ${action('Read +2')} ${muted('complete')} ${muted('2 files +23 -9')}  ${muted('20 events; claude-code; tools: Read, Edit, Bash')}`,
  `${lane('|', '', '|', '|', '|')}${muted('Prompt:')} "Release the claim only for retryable failures."`,
  `${lane('|', '', '|', '|', '|')}`,
  `${lane('|', '', '|', '*', '|')}${hash('993f79127546')} - 14:28 ${session('[codex 14:28]', 3)} turn 1      ${action('Read +3')} ${muted('complete')} ${muted('3 files +46 -2')}  ${muted('23 events; codex; tools: Read, Write, Edit, Bash')}`,
  `${lane('|', '', '|', '|', '|')}${muted('Prompt:')} "Define provider timeout configuration."`,
  `${lane('|', '', '|', '|', '|')}`,
  `${lane('|', '', '|', '', '*')}${hash('68459d51926d')} - 14:24 ${session('[codex 14:12]', 4)} turn 2      ${action('Read +2')} ${muted('complete')} ${muted('1 file +63 -7')}  ${muted('25 events; codex; tools: Read, Edit, Bash')}`,
  `${lane('|', '', '|', '', '|')}${muted('Prompt:')} "Synchronize eight delivery workers in the integration test."`,
  `${lane('|', '', '|', '', '|')}`,
  `${lane('*', '', '|', '', '|')}${hash('ad8a917cf3bd')} - 14:20 ${session('[claude 14:20]', 0)} turn 1      ${action('Read +2')} ${muted('complete')} ${muted('no file changes')}  ${muted('17 events; claude-code; tools: Read, VectorSearch, Bash')}`,
  `${lane('|', '', '|', '', '|')}${muted('Prompt:')} "Map the existing fulfillment boundary."`,
  `${lane('|', '', '|', '', '|')}`,
  `${lane('', '', '*', '', '|')}${hash('deef09f4d71a')} - 14:16 ${session('[claude 14:16]', 2)} turn 1      ${action('Read +2')} ${muted('complete')} ${muted('no file changes')}  ${muted('15 events; claude-code; tools: Read, VectorSearch, Bash')}`,
  `${lane('', '', '|', '', '|')}${muted('Prompt:')} "Trace the parsed request-body path."`,
  `${lane('', '', '|', '', '|')}`,
  `${lane('', '', '', '', '*')}${hash('b31b0e743646')} - 14:12 ${session('[codex 14:12]', 4)} turn 1      ${action('Read +2')} ${muted('complete')} ${muted('1 file +71 -0')}  ${muted('20 events; codex; tools: Read, Write, Bash')}`,
  `${lane('', '', '', '', '|')}${muted('Prompt:')} "Add the first concurrent delivery test."`,
  '',
];

export const graphOutput = graphLines.join('\n');

export const sessionsLines = [
  `${label('sessions')} 5 ${muted('recorded')}`,
  '',
  `${success('[COMPLETE]')} ${label('claude-a13f')}`,
  `  ${label('adapter')} claude-code ${muted('/')} ${warning('claude-opus-4-8')} ${muted('/ default')}`,
  `  ${label('turns')}   4 total, ${success('4 complete')}`,
  `  ${label('events')}  48`,
  `  ${label('activity')} ${muted('14:42 -> 14:48')}`,
  `  ${label('head')}     turn 4 ${success('post')} ${hash('9df3a1e9c54b')}`,
  `  ${label('prompt')}   ${warning('"Make the webhook idempotency claim transactional and release it only on retryable failures."')}`,
  `  ${label('tools')}    ${action('Read, Edit, Bash')}`,
  '',
  `${success('[COMPLETE]')} ${label('codex-b91e')}`,
  `  ${label('adapter')} codex ${muted('/')} ${warning('gpt-5.3-codex')} ${muted('/ workspace-write')}`,
  `  ${label('turns')}   4 total, ${success('4 complete')}`,
  `  ${label('events')}  44`,
  `  ${label('activity')} ${muted('13:45 -> 14:43')}`,
  `  ${label('head')}     turn 4 ${success('post')} ${hash('7a2c44f108bd')}`,
  `  ${label('prompt')}   ${warning('"Keep duplicate deliveries observable without running fulfillment twice."')}`,
  `  ${label('tools')}    ${action('Read, Write, Edit, Bash')}`,
  '',
  `${success('[COMPLETE]')} ${label('claude-c702')}`,
  `  ${label('adapter')} claude-code ${muted('/')} ${warning('claude-sonnet-4-6')} ${muted('/ default')}`,
  `  ${label('turns')}   4 total, ${success('4 complete')}`,
  `  ${label('events')}  39`,
  `  ${label('activity')} ${muted('13:34 -> 14:34')}`,
  `  ${label('head')}     turn 4 ${success('post')} ${hash('cc8e305da912')}`,
  `  ${label('prompt')}   ${warning('"Add an integration test for two workers claiming the same Stripe event."')}`,
  `  ${label('tools')}    ${action('Read, Write, Edit, Bash')}`,
  '',
  `${success('[COMPLETE]')} ${label('codex-d44a')}`,
  `  ${label('adapter')} codex ${muted('/')} ${warning('gpt-5.3-codex')} ${muted('/ workspace-write')}`,
  `  ${label('turns')}   3 total, ${success('3 complete')}`,
  `  ${label('events')}  31`,
  `  ${label('activity')} ${muted('13:40 -> 14:25')}`,
  `  ${label('head')}     turn 3 ${success('post')} ${hash('d0f4a83c6211')}`,
  `  ${label('prompt')}   ${warning('"Move provider timeouts into config and validate them at startup."')}`,
  `  ${label('tools')}    ${action('Read, Write, Edit, Bash')}`,
  '',
  `${success('[COMPLETE]')} ${label('claude-e8b5')}`,
  `  ${label('adapter')} claude-code ${muted('/')} ${warning('claude-sonnet-4-6')} ${muted('/ default')}`,
  `  ${label('turns')}   3 total, ${success('3 complete')}`,
  `  ${label('events')}  27`,
  `  ${label('activity')} ${muted('13:29 -> 14:12')}`,
  `  ${label('head')}     turn 3 ${success('post')} ${hash('6d93ae201f4c')}`,
  `  ${label('prompt')}   ${warning('"Instrument checkout confirmation with request, event, and order ids."')}`,
  `  ${label('tools')}    ${action('Read, Write, Edit')}`,
];

export const sessionsOutput = sessionsLines.join('\n');

const initLines = [
  `initialized hidden git repo: ${muted('/Users/aadijo/Dev/relay-api/.turnal/git')}`,
  `worktree id: ${muted('wt_1d994d3024c8')}`,
  `workspace git already configured: ${muted('/Users/aadijo/Dev/relay-api/.git')}`,
  `updated gitignore: ${muted('/Users/aadijo/Dev/relay-api/.gitignore')}`,
  `configured claude hooks: ${muted('/Users/aadijo/Dev/relay-api/.claude/settings.json')}`,
  `configured codex hooks: ${muted('/Users/aadijo/Dev/relay-api/.codex/config.toml')}`,
];

const searchLines = [
  `${label('claude-a13f:4')}  ${muted('worktree=wt_1d994d3024c8')}  claude-code  ${muted('2026-07-11 14:48:22 UTC')}`,
  `  prompt: Make the webhook idempotency claim transactional and release it only on retryable failures.`,
  `  assistant: Moved the claim and fulfillment dispatch into one transaction with retry-safe release behavior.`,
  `  tools: ${action('Read, Edit, Bash')}`,
  `  files: src/webhooks/stripe.ts, test/webhooks/stripe.test.ts`,
  `  match: ... webhook ${warning('[idempotency]')} claim is committed before fulfillment dispatch ...`,
  '',
  `${label('codex-b91e:3')}  ${muted('worktree=wt_1d994d3024c8')}  codex  ${muted('2026-07-11 14:30:07 UTC')}`,
  `  prompt: Exercise eight concurrent invoice.paid deliveries and prove fulfillment runs once.`,
  `  assistant: Added a barrier-synchronized test covering the idempotency key under eight concurrent deliveries.`,
  `  tools: ${action('Read, Write, Bash')}`,
  `  files: test/webhooks/stripe.test.ts`,
  `  match: ... the same ${warning('[idempotency]')} key is claimed by eight workers ...`,
  '',
  `${label('claude-c702:3')}  ${muted('worktree=wt_1d994d3024c8')}  claude-code  ${muted('2026-07-11 14:16:41 UTC')}`,
  `  prompt: Make the claim insert use ON CONFLICT without hiding real database errors.`,
  `  assistant: Duplicate idempotency claims return false while connection and constraint errors still surface.`,
  `  tools: ${action('Read, Edit, Bash')}`,
  `  files: src/payments/event-claims.ts`,
  `  match: ... insert the ${warning('[idempotency]')} key with ON CONFLICT DO NOTHING ...`,
];

const blameLines = [
  `${label('14:48 [claude 14:42] turn 4')}     24 |   if (!claimed) return res.sendStatus(200)`,
  `  ${muted('Prompt:')} "Make the webhook idempotency claim transactional and release it only on retryable failures."`,
  `  ${muted('session:')} claude-a13f`,
  `  ${muted('adapter:')} claude-code`,
  `  ${muted('tools:')} Read, Edit, Bash`,
  `  ${muted('checkpoint:')} refs/agent-vcs/checkpoints/claude-a13f/turn/000004/post`,
  `  ${muted('id:')} 9df3a1e9c54b`,
];

const rollbackLines = [
  `${success('Rollback complete')}`,
  `  target: claude-a13f:turn:4:pre`,
  `  id:     ${hash('e4b611c27fa0')}`,
  `  ref:    ${muted('refs/agent-vcs/checkpoints/claude-a13f/turn/000004/pre')}`,
  '',
  `${success('Previous workspace saved')}`,
  `  id:  ${hash('80f4f334992a')}`,
  `  ref: ${muted('refs/agent-vcs/rollback-safety/claude-a13f/turn/000004/pre/1783629131200000000-a4c9e2f18b7d')}`,
];

const replayLines = [
  `${label('replay worktree:')} /Users/aadijo/.turnal/replays/claude-a13f-turn-000003-post`,
  `${label('state:')} claude-a13f turn 3 ${success('post')}`,
  `${label('commands:')}`,
  `  turnal replay next`,
  `  turnal replay prev`,
  `  turnal replay goto claude-a13f:turn:3:post`,
  `  turnal replay diff`,
  `  turnal replay show`,
  `  turnal replay keep`,
  `  turnal replay stop`,
];

const verifyLines = [
  `${label('target:')} claude-a13f:4:post`,
  `${label('state:')}  ${hash('59a91d6d715f3b86d32824e589a9fe30d73b2f1a')}`,
  `${label('checks:')} ${success('3 passed')}, 0 failed, 0 timed out, 0 could not start, 0 infrastructure errors`,
  '',
  `${success('PASS')}    unit-tests                  ${muted('2.84s')}`,
  `${success('PASS')}    typecheck                   ${muted('1.19s')}`,
  `${success('PASS')}    lint                        ${muted('684ms')}`,
  '',
  `${label('limitations:')}`,
  `  - Checks ran in the current toolchain against an isolated checkpoint worktree.`,
];

const saveLines = [
  `${success('Saved checkpoint')} ${hash('e8924b927c11')}`,
  `  ${label('hash:')} e8924b927c11e987d304372ff3bb8e57b93a5d42`,
  `  ${label('message:')} "tests passing before webhook refactor"`,
  `  ${label('rollback:')} turnal rollback --to e8924b927c11e987d304372ff3bb8e57b93a5d42`,
];

const forkLines = [
  `${label('fork readiness:')} ${warning('needs_context')}`,
  `${label('target:')}         claude-a13f:turn:4:pre`,
  `${label('fidelity:')}       L1`,
  `${label('source turn:')}    claude-a13f:4 ${success('(complete)')}`,
  `${label('model:')}          claude-opus-4-8`,
  `${label('base:')}           refs/agent-vcs/checkpoints/claude-a13f/turn/000004/pre`,
  `${label('captured files:')} 184`,
  `${label('instruction:')}    ${success('available')}`,
  `  Return success for an event another worker already completed.`,
  `${label('conditions:')}`,
  `  workspace files  ${success('exact')}                    184 captured files`,
  `  conversation     ${warning('not_recorded')}             prior context must be supplied`,
  `  toolchain        ${warning('not_recorded')}             use the current environment`,
  `  secrets          ${warning('reauthorization_required')} secret values are never replayed`,
  `  network          ${warning('live')}                     external services may differ`,
  `  evaluators       ${success('configured')}               3 repository checks`,
];

const caseLines = [
  `${success('created task')} task_0123456789abcdef0123456789abcdef`,
  `${success('created case')} case_fedcba9876543210fedcba9876543210`,
  `${label('task:')}           task_0123456789abcdef0123456789abcdef revision 1`,
  `${label('source turn:')}    claude-a13f:4`,
  `${label('base commit:')}    ${hash('e4b611c27fa0')}`,
  `${label('instruction:')}    ${success('available')}`,
  `${label('readiness:')}      ${warning('needs_context')}`,
  `${label('fidelity:')}       L1`,
  `${label('verifiers:')}      unit-tests, typecheck, lint`,
  `${label('attempts:')}       none linked`,
];

export const commandPanels: CommandPanel[] = [
  {
    id: 'blame',
    label: 'turnal blame',
    command: 'turnal blame src/webhooks/stripe.ts:24 --verbose',
    description: 'Trace the line under investigation to the completed turn, prompt, tools, and checkpoint that produced it.',
    output: blameLines.join('\n'),
  },
  {
    id: 'verify',
    label: 'turnal verify',
    command: 'turnal verify claude-a13f:4:post',
    description: 'Run repository-defined checks against the live workspace or an isolated historical checkpoint.',
    output: verifyLines.join('\n'),
  },
  {
    id: 'save',
    label: 'turnal save',
    command: 'turnal save "tests passing before webhook refactor"',
    description: 'Name an explicit rollback point in hidden Git without adding a commit to your project history.',
    output: saveLines.join('\n'),
  },
  {
    id: 'fork',
    label: 'turnal fork --dry-run',
    command: 'turnal fork claude-a13f:4 --dry-run',
    description: 'Inspect which inputs a faithful rerun would have and which context must still be supplied.',
    output: forkLines.join('\n'),
  },
  {
    id: 'case',
    label: 'turnal case create',
    command: 'turnal case create claude-a13f:4',
    description: 'Preserve a turn as an immutable experimental case with its starting state, task, verifiers, and limitations.',
    output: caseLines.join('\n'),
  },
  {
    id: 'replay',
    label: 'turnal replay',
    command: 'turnal replay checkout claude-a13f:3:post',
    description: 'Inspect a checkpoint in an isolated replay worktree, move through adjacent states, and keep only what you need.',
    output: replayLines.join('\n'),
  },
];

export const workflowScenes: WorkflowScene[] = [
  {
    id: 'init',
    title: 'Initialize Turnal',
    command: 'turnal init --agent all',
    output: initLines,
    durationMs: 6000,
  },
  {
    id: 'sessions',
    title: 'See every active trail',
    command: 'turnal sessions',
    output: sessionsLines,
    durationMs: 11000,
  },
  {
    id: 'log',
    title: 'Read the interleaved history',
    command: 'turnal log --all-worktrees',
    output: graphLines,
    durationMs: 12000,
    mode: 'pager',
  },
  {
    id: 'search',
    title: 'Find a specific moment',
    command: 'turnal search "idempotency key"',
    output: searchLines,
    durationMs: 8000,
  },
  {
    id: 'blame',
    title: 'Trace a line to its prompt',
    command: 'turnal blame src/webhooks/stripe.ts:24 --verbose',
    output: blameLines,
    durationMs: 7000,
  },
  {
    id: 'rollback',
    title: 'Roll back with a safety net',
    command: 'turnal rollback claude-a13f:turn:4:pre',
    output: rollbackLines,
    durationMs: 7000,
  },
];
