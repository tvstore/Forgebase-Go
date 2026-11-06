# AGENTS.md — Forgebase project instructions

Permanent instructions for every agent (and human) working in this repository. This file is the **single source of truth** for the workflow and the model selection policy. Other documents link here instead of repeating it.

## 1. Overview and guiding principle

Forgebase is a Rust framework for building business applications. It is inspired by the concepts and capabilities of the Frappe Framework. Current project status is kept in [README.md](README.md); see also [docs/blueprint/vision.md](docs/blueprint/vision.md).

> Build for longevity, not complexity. Maintenance is the most expensive part of software ownership.

The highest priority is ease of maintenance and low long-term maintenance cost. Do not chase many features, clever abstractions or complex architecture when a simpler solution already meets the need.

## 2. Core engineering principles

1. **Simplicity first.** Choose the simplest solution that still meets the requirement.
2. **Maintainability by default.** Code must be easy for other developers to read, understand, test and fix.
3. **Modular monolith first.** Separate modules by clear responsibility. Avoid microservices and fragmentation that is not yet needed.
4. **Avoid overengineering.** Do not build abstractions, plugin systems or generic configuration before a real need exists.
5. **Dependency discipline.** Evaluate need, stability, maintenance, compatibility and license of every dependency.
6. **Testing matters.** Add tests proportional to risk and run the available checks.
7. **Documentation is part of implementation.** Document important decisions, limits and usage.
8. **No unnecessary rewrites.** Do not change working parts without a clear, measurable reason.
9. **No unsupported assumptions.** Check the repository and documentation first. If something is not known, state it as needing verification.
10. **Incremental delivery.** Build capabilities step by step and make sure the foundation is stable before widening scope.

Do not copy Frappe source code. Implement designs independently.

Permanent owner constraints:

- Do not choose a dependency only because it is popular; record important decisions as ADRs.
- Start as a modular monolith. Do not build dynamic libraries, a WASM plugin runtime or a complex extension system unless an approved requirement needs it.
- The intended feature scope in the docs is a direction for exploration, not permission to implement everything at once.
- Verify dependency licenses and their compatibility, and document the license decision.
- The project is open source under the MIT licence (the repository is public since 2026-10-31).

## 3. Mandatory four-stage workflow

Every task goes through four stages. Each stage has a stop condition. Silence from the user is never approval.

### Stage 1 — Agent Plan

- Understand the requirement, inspect the repository, read the instructions.
- Define scope, non-goals, acceptance criteria and risks; list affected files and the tests and documentation needed; choose the simplest solution.
- **Output:** summary, plan, scope and non-goals, acceptance criteria, risks and test strategy.
- **STOP.** Do not implement before the user approves the plan.

### Stage 2 — Agent Doing

- Implement only the approved plan. Keep scope. Write or update tests and documentation.
- Run formatter, linter, tests and build checks.
- Report the actual changes, test results and limitations.
- If a change to requirements, architecture, scope or a confirmed decision is needed, **stop** and propose a plan change.
- **Output:** implementation, file summary, test evidence, unfinished items.

### Stage 3 — Agent Review

- Independent: use a separate agent or session with access to the diff, the requirements and the test results where possible.
- Check: conformance to requirements and acceptance criteria, correctness, security and authorization, regressions and compatibility, test quality, unnecessary complexity, maintenance cost, dependencies and licenses, documentation consistency.
- Do not pass a change merely because it compiles.
- **Output:** findings by severity with location and reason, recommendations, and a status of **PASS** or **NEEDS CHANGES**.
- If code changes are needed, return to Stage 2, then repeat Stage 3.

### Stage 4 — Agent Confirmation

- Summarize the result and the review; state which tests passed, failed or could not be run; list remaining risks and findings; update status and checklists; re-run safe checks; prepare a draft PR, changelog or release notes if in scope and permitted.
- **Must stop and ask for approval** when it would: change code or results already produced; change requirements, architecture or confirmed decisions; expand scope; fix code after review; or take an action not yet authorized (merge to `main`, publish a release).
- **Final output:** status, implementation summary, review result, test evidence, remaining risks and work, decisions needed.

## 4. Agent separation

Keep Plan, Doing and Review separate. Do not rely on the implementing agent to declare its own work correct. If no separate agent or session is available, use an independent review context as far as possible and state the limitation.

## 5. Git and repository safety

- Do not change `main` directly without permission.
- Do not merge a PR or publish a release without explicit approval. The standing merge authorization in [section 10](#10-operating-model) is such an approval, limited to its conditions.
- Do not delete data, files or configuration without a reason and permission.
- Do not force push or perform other destructive operations without approval.
- Do not silently change files outside the approved scope.
- Check `git status` before and after working.
- Do not overwrite the user's changes; report unrelated changes you find.

## 6. Documentation structure

| Path | Purpose |
|---|---|
| [AGENTS.md](AGENTS.md) | Permanent project instructions, agent workflow and operating model (this file) |
| [README.md](README.md) | Purpose, status, how to get started |
| [CONTRIBUTING.md](CONTRIBUTING.md) | How to build, run local checks and contribute |
| [CHANGELOG.md](CHANGELOG.md) | Notable changes, Keep a Changelog format |
| [docs/blueprint/](docs/blueprint/) | `vision`, `feature-matrix`, `architecture`, `dependency-evaluation`, `security-model`, `milestones`, `maintenance-policy` |
| [docs/README.md](docs/README.md) | Index of all documentation (guides, blueprint, ADRs, records) and the project status |
| [docs/guide/](docs/guide/first-app.md) | Guides for people who build on Forgebase and run it: [getting started](docs/guide/getting-started.md), [first app](docs/guide/first-app.md), [CLI](docs/guide/cli.md), [HTTP API](docs/guide/http-api.md), [desk](docs/guide/desk.md), [workflow and reports](docs/guide/workflow-and-reports.md), [customizing and restricting data](docs/guide/customizing.md), [integration](docs/guide/integration.md), [operations](docs/guide/operations.md), [deploy](docs/guide/deploy.md) |
| [docs/adr/](docs/adr/README.md) | Architecture Decision Records |
| [docs/owner-decisions.md](docs/owner-decisions.md) | The owner's decisions, comments and way of thinking, with links to the detailed records; read before planning, keep current |

Do not duplicate content across documents; link instead.

## 7. Communication style

- Communicate with the project owner in **Indonesian**.
- Use **English** for code, symbols, commit messages and technical documentation.
- Distinguish verified facts, assumptions and recommendations.
- Do not claim a test was run or a review was done without evidence.

## 8. Final Rule

If implementation speed conflicts with long-term maintainability, choose maintainability. If an important decision is unclear, ask a question or present concrete options. Do not guess and then implement.

Every decision or comment the owner gives is recorded in [docs/owner-decisions.md](docs/owner-decisions.md) (rules at the top of that file). Read it before planning. It informs recommendations; it does not replace asking the owner.

## 9. Model Selection Policy

### 9.1 Objective

Select the most appropriate Claude model for each stage of the Forgebase workflow.

Forgebase prioritizes long-term maintainability, correctness, simplicity, and low maintenance costs over implementation speed alone.

Use stronger models for decisions with long-term architectural impact. Use faster, more economical models for routine and mechanical work.

### 9.2 Model assignment

Use the following model assignments when the models are available in the current Claude Code environment:

| Workflow Stage | Preferred Model | Responsibility |
|---|---|---|
| Stage 1 — Agent Plan | Opus 5.5 | Requirement analysis, architecture, scope, risk assessment, acceptance criteria, and implementation planning |
| Stage 2 — Agent Doing | Sonnet 5.5 | Routine implementation, tests, documentation, and scoped bug fixes |
| Stage 3 — Agent Review | Opus 5.5 | Independent code review, architectural correctness, security, regressions, and maintainability |
| Stage 4 — Agent Confirmation | Sonnet 5.5 or Haiku 5.5 | Summaries, checklists, status reports, and permitted administrative follow-up |
| Complex problem escalation | Fable 5.1 | Difficult cross-module reasoning, unresolved architectural trade-offs, or problems that remain unresolved after a reasonable attempt |

These assignments are defaults, not guarantees that a model is available or that every task requires the same reasoning effort.

### 9.3 Model selection rules

**Opus 5.5 — Architecture and high-impact decisions.** Use Opus 5.5 for:

- initial project research and architecture planning;
- database and DocType metadata design;
- document lifecycle and transaction boundaries;
- authentication, authorization, and permission models;
- multi-site isolation and data integrity;
- migration strategies and backward compatibility;
- decisions that would be expensive to reverse;
- independent review of high-impact changes.

Do not downgrade these tasks to a smaller model solely to save time or usage.

**Sonnet 5.5 — Standard implementation.** Use Sonnet 5.5 for:

- implementing approved plans;
- routine module and API development;
- writing unit and integration tests;
- documentation updates;
- normal bug fixes and refactoring within approved scope;
- addressing review findings that do not require a new architectural decision.

If implementation reveals a fundamental design issue, stop and return to Stage 1 rather than inventing a new architecture.

**Haiku 5.5 — Lightweight tasks.** Use Haiku 5.5 for:

- summarizing completed work;
- updating status reports and checklists;
- formatting documentation;
- mechanical, low-risk changes;
- preparing draft release notes or PR summaries.

Do not use Haiku as the sole reviewer for security-sensitive, architectural, data-integrity, or high-impact changes.

**Fable 5.1 — Escalation only.** Use Fable 5.1 selectively when:

- Opus 5.5 cannot resolve a difficult architectural question;
- multiple interacting constraints require deeper investigation;
- a critical issue remains unresolved after a documented attempt;
- a complex design comparison could materially affect long-term maintenance.

Before escalating, summarize the problem, known evidence, previous attempts, constraints, and unresolved questions. Fable must not independently override approved requirements, architecture decisions, or user-confirmed results.

### 9.4 Model availability and fallback

Before assigning a model, verify that it is available in the current Claude Code environment. If the preferred model is unavailable:

1. Use the strongest suitable available model for the task.
2. Preserve the same workflow stage and approval requirements.
3. Clearly disclose the substitution when reporting the result.
4. Never claim that a specific model was used unless it was actually selected and used.

Do not repeatedly attempt unavailable models or change the workflow to bypass model restrictions.

### 9.5 Agent independence

Stage 1, Stage 2, and Stage 3 have different responsibilities. Plan defines the approved scope and acceptance criteria. Doing implements only the approved plan. Review independently evaluates the actual changes and evidence. Confirmation reports the outcome and performs only permitted follow-up actions.

Using different models does not automatically guarantee independent review. When possible, run Review in a separate agent or session with access to the requirements, actual diff, and test results. The reviewer must not simply trust the implementer's claims or summary.

### 9.6 Approval gates

Model selection never overrides the mandatory four-stage workflow. Stage 1 must stop and wait for explicit user approval before implementation. Stage 2 must stay within the approved scope. Stage 3 must return findings to Stage 2 when code changes are necessary. Stage 4 may perform permitted administrative follow-up within scope, but must not change previously created or confirmed results or decisions without approval. Merging into main, publishing releases, and other consequential actions require explicit authorization unless the user has already authorized that exact action.

### 9.7 Model usage transparency

At the beginning of each stage, state:

- the current workflow stage;
- the model actually being used, if verifiable;
- the objective of the stage;
- whether user approval is required before proceeding.

At the end of each stage, report:

- work completed;
- tests and checks performed;
- review or implementation status;
- any model substitution;
- any unresolved issue requiring user input.

Do not claim that model selection alone guarantees correctness. Evidence, tests, independent review, and explicit approval remain mandatory.

### 9.8 First session

For the first Forgebase session:

- use Opus 5.5 for Stage 1 — Agent Plan, if available;
- perform research and inspect the actual repository;
- prepare the proposed research and blueprint plan;
- do not begin implementation or create blueprint documents until the user approves the plan.

The highest priority remains: minimize long-term maintenance costs through simple architecture, clear boundaries, good tests, careful dependency selection, and documented decisions.

See also [ADR 0008](docs/adr/0008-model-selection-policy.md) (records the adoption of this policy; this section is the normative text).

## 10. Operating model

Owner decision of 2026-10-28, confirmed by the owner on 2026-10-29. It changes who carries out the workflow, not the workflow itself: sections 3 to 9 still apply. The standing merge authorization in section 10.3 is the prior approval that section 3 (Stage 4), section 5 and section 9.6 require, for merges that meet its conditions and for nothing else.

### 10.1 Sessions

- **Information session.** The original planning session holds approved plans and confirmed results. New plans, requirement changes and owner decisions are made there.
- **Working session.** A separate session executes approved plans: implementation, review, pull requests, CI fixes and merging within the authorization in section 10.3. After each merge, each deviation from the plan and each blocker it sends the information session a short report: what was done, the evidence (tests, CI, review status, PR link) and any deviation with its reason.

### 10.2 Roles in the working session

- The orchestrator (Opus) writes specifications and coordinates.
- Stage 2 Doing is a Sonnet subagent.
- Stage 3 Review is a new Opus subagent for each review round. It did not write the change and receives the specification and the diff, not the implementer's summary. Findings go back to Stage 2 and the review repeats until PASS. A minor documentation-only fix may be applied directly by the orchestrator and is disclosed.
- To keep CI cost down (owner decision of 2026-10-30), the working session opens pull requests as **draft** (CI skips drafts), runs all checks locally during implementation and review, and marks the pull request ready only after review PASS. See [the maintenance policy](docs/blueprint/maintenance-policy.md#ci).

### 10.3 Standing merge authorization

The working session may merge a pull request into `main` without asking again, using a merge commit, only when all of these hold:

1. the work is part of a plan the owner approved;
2. CI is green on the pull request head;
3. an independent review (section 10.2) reports PASS on the diff being merged. A code change pushed after that PASS (a CI fix included) needs a new review round; only a disclosed documentation-only fix (section 10.2) is exempt.

Red or unreviewed work is never merged. Force pushes to `main` are never allowed. Publishing a release still needs explicit approval.

### 10.4 Deviations (PDCA)

When execution deviates from the approved plan, the working session applies Plan → Do → Check → Act and reports the deviation. It does not take decisions that belong to the owner: new requirements, architecture changes beyond the plan, significant new dependencies, changes to Accepted ADRs, and starting a milestone that is not in an approved plan. It reports such an item to the information session and pauses that work until the owner decides.
