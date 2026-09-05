# Token usage and cost estimates

Turnal records provider-reported token usage on completed assistant turns. Run `turnal usage` inside a recorded project for a session breakdown, or open the Usage tab in Turnal Prism for a machine-wide project breakdown.

```sh
turnal usage
turnal usage --json
turnal ui
```

## Coverage

Built-in Claude Code and Codex capture reads cumulative usage when each prompt arrives, then stores the completed turn's delta in the durable event log. Resumed sessions exclude usage from before that prompt. External protocol-v1 adapters can attach `usage` to an `assistant.message` event. Existing events are not rewritten, so usage begins with turns captured after this feature is installed.

Every summary includes `covered_turns` and `total_turns`. Missing usage stays missing instead of being treated as zero. Turns without a readable starting baseline or a new usage reading remain uncovered, as do turns whose counters reset. Unknown models retain their token counts but do not receive a cost estimate.

Transcript usage reads are limited to 16 MiB so usage collection cannot scan an unbounded file while capture holds the session lock. Larger transcripts remain uncovered; Turnal continues recording agent events without usage totals. A transcript that grows past the limit during a read is also rejected rather than counted partially.

The normalized categories are fresh input, cache reads, cache writes, output, reasoning output, and API calls. Codex API-call counts are unavailable because usage notifications do not reliably identify requests. Reasoning tokens are informational because Codex includes them in output tokens. Turnal excludes them from the total to prevent double counting.

## Cost estimates

Cost is an API-equivalent estimate, not a bill. Subscription allowances, credits, fast mode, batch discounts, regional processing, long-context multipliers, and negotiated pricing can change the amount charged by a provider.

The bundled rate table is versioned `2026-09-04`. It follows the public [OpenAI model pricing](https://developers.openai.com/api/docs/models/gpt-5.6-sol), [OpenAI ChatGPT and Codex rate card](https://help.openai.com/en/articles/20001415-chatgpt-rate-card-enterprise-token-based-pricing), and [Anthropic model pricing](https://platform.claude.com/docs/en/about-claude/pricing). A future Turnal release can update rates without changing the recorded token facts.

## Privacy

Usage counters and estimates stay in the Turnal store and its disposable local indexes. The feature makes no network requests and does not send prompts, transcripts, or usage to either provider.
