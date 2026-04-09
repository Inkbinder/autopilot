# Autopilot Demo Todo App

This repository is seeded for the Autopilot engineering demo.

The starter is intentionally minimal. It keeps only the static entry point,
test harness, CI wiring, and file boundaries needed for parallel Autopilot
work without pre-building the actual app.

Constraints:

- Build the product with plain JavaScript modules and web components only.
- Do not add React, Vue, Svelte, Lit, or any other application framework.
- Keep the app runnable directly from static files so it can be served with `npx serve .`.

Development commands:

```bash
npm install
npm test
npx serve . -l 4173
```
