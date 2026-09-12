# Security policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security Advisories for this repository. Do not open a public issue containing credentials, captured prompts, raw hook payloads, or a proof of concept that exposes another user's data.

Include the affected Turnal version, operating system, reproduction steps, impact, and any proposed mitigation. Maintainers should acknowledge a report within three business days and provide a status update at least weekly until resolution.

## Security model

Turnal stores agent activity locally under `.turnal`. New metadata directories are created with owner-only permissions and sensitive records with owner read/write permissions where the platform supports POSIX modes. Snapshot deny globs apply to checkpoint and workspace-Git capture, but users must still avoid committing secrets to their ordinary Git repository.

Turnal is a local developer tool, not a multi-tenant security boundary. Anyone with access to the user account, workspace, backups, or underlying disk may be able to read retained data. Use encrypted storage and operating-system access controls for sensitive workspaces.

Shared-history redaction is a best-effort safety layer, not a guarantee. Run `turnal share redaction diagnose`, review project-specific examples with `turnal share redaction review`, and inspect the complete publication preview before approval. Novel low-entropy secrets can evade detection, so avoid putting sensitive values in agent context and rotate any credential found in published history.

Only the latest stable release receives security fixes. Recovery and retention behavior is documented under `docs/`.
