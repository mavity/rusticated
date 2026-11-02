# Two-Stroke Tooling: Helper-Only Main Loop

## Goal

Replace the main agent's existing tool set with a single tool, `helper`. The main agent delegates tool-using work to a fresh helper conversation, while retaining responsibility for understanding the request, choosing the task, and supplying the helper with the context it needs.

## Tool Interface

The main agent exposes exactly one tool:

```text
helper(task, description)
```

- `task` states what the helper must do. It should be concrete and actionable.
- `description` supplies the detailed context the helper needs to understand and complete that task. It should be populated rigorously, including relevant requirements and constraints.

Both arguments are strongly encouraged to be thorough. The task describes the requested work; the description provides its supporting context.

## Helper Conversation

Every invocation starts a brand-new conversation. It does not inherit the main agent's conversation history.

- Put `description` in the helper's system prompt.
- Put `task` in the helper's user prompt.
- Give the helper access to all tools currently available to the main agent.
- The helper performs the delegated work using those tools and produces a final text response.

The main agent's tool surface remains limited to `helper`; existing tools are available only inside the helper conversation.

## Returning the Result

When the helper finishes, return its final text output and, if it made tool calls, its last normalized tool response as the result of the `helper` tool call. The result has `final_text` and optional `last_tool_response` fields; the latter contains the tool `name` and its `response`. If no tool call was made, omit `last_tool_response`.

## Expected Interaction

1. The main agent identifies work that requires tools and formulates a clear `task`.
2. The main agent supplies the helper with the detailed context and constraints in `description`.
3. The helper starts a fresh conversation, receives those values in their designated prompts, and uses the existing tools to do the work.
4. The helper's final text and optional last tool response are delivered back to the main agent.
5. The main agent uses that result to continue its response or work.

## Requirements

- The main agent exposes no tools other than `helper`.
- Each helper call creates an isolated, fresh conversation.
- The `description` is part of the helper's system prompt, and the `task` is part of its user prompt.
- The helper has access to the existing tools.
- The helper result includes its final text and its last tool response when one exists.
- The main agent must include sufficient context in `description`, since the helper cannot rely on the main conversation history.
