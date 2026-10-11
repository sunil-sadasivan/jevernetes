# Legacy Python companion

This package retains the dashboard, TUI, semantic search, review, context and investigation-prompt workflows, along with their current Python collection dependencies. The production default runtime is the Rust `jevernetes` binary in `src/`.

Run this companion explicitly with `python3 -m jevernetes` or the installed `jevernetes-legacy` command. Rust never imports or shells out to it. See [migration and removal gates](../docs/migration.md) before deleting any of these modules.
