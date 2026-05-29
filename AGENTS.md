# HARD RULES

- This project uses **async i/o**, do NOT try to port anything back to blocking i/o.
- This project uses **custom std/sysroot** (rusticated) for dependent crates demo, brot, washmhost, loch and possibly others.
- Do NOT use terminal commands for accessing files: reading contents, searching, writing, patching. ALWAYS use built-in tools for that, not terminal commands.
- Do NOT embark on refactorings without consulting. Planning must be done FIRST, and discussed. Until the plan is agreed upon, no code changes should be made.

## Advice

Follow [README.md](README.md) to understand custom sysroot/std build practice.