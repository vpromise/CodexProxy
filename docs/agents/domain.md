# Domain Docs

This repository uses a single-context layout: `GLOSSARY.md` at the repository root and architecture decision records in `docs/adr/`.

## Before exploring

Read the root `GLOSSARY.md` and ADRs in `docs/adr/` that concern the area being explored.

If these files do not exist, proceed silently. The `domain-modeling` skill creates them lazily when terminology or decisions are resolved.

## File structure

```text
/
├── GLOSSARY.md
└── docs/
    └── adr/
        └── NNNN-decision-title.md
```

## Use the glossary's vocabulary

Use glossary terms in issue titles, proposals, hypotheses, and test names. When a needed concept is absent, check existing project terminology; record a genuine gap for `domain-modeling`.

## Surface ADR conflicts

If a proposal contradicts an existing ADR, identify the ADR and explain why the decision should be revisited.
