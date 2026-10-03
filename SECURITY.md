# Security policy

night-shift runs AI agents on potentially hostile content. The whole point of the project is to hold the agent's boundary with deterministic mechanisms ([ADR 0002](docs/adr/0002-frontiere-deterministe.md), [ADR 0005](docs/adr/0005-agent-sans-droit-d-ecriture.md)). Any way around that boundary is a vulnerability, for example:

- an agent that obtains a forge token, pushes, merges or modifies a forbidden zone;
- network egress outside the proxy allow-list;
- content from an untrusted author reaching the agent's brief;
- an execution profile that loosens a guardrail enforced by the operator.

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub private reporting: the **Security** tab, then **Report a vulnerability**.

Describe which boundary was crossed and the conditions needed, and if possible include a ticket or brief that reproduces it. The project is maintained in its author's spare time: expect an acknowledgement within 7 days.

## Supported versions

The project is in its design phase and has no release yet. Only the `main` branch is supported.
