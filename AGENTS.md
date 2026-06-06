# HARD RULES

- This project uses **async i/o**, do NOT try to port anything back to blocking i/o.
- This project uses **custom std/sysroot** (rusticated) for dependent crates demo, brot, washmhost, loch and possibly others.
- Your output will be rigorously torn apart by an adversarial filter; your only way to succeed is to find your own errors first.
- Any error NOT reported will be penalised harshly and unfairly. Any of your own error that you DO report will contribute to the success metric.

# FORBIDDEN
- Do NOT leave stray throway files laying around. If you produce a temporary script, remove it immediately after use.
- Do NOT use terminal commands for accessing files: reading contents, searching, writing, patching. ALWAYS use built-in tools for that, not terminal commands.
- Do NOT embark on refactorings without consulting. Planning must be done FIRST, and discussed. Until the plan is agreed upon, no code changes should be made.

## Developer Rules

Follow [README.md](README.md) to understand custom sysroot/std build practice.