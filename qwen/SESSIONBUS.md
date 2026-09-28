# Sessionbus in Qwen

In a `qwen-peer` session, use the granted `mcp__sessionbus__sessionbus` tool with
`{action, arguments}`. Its `list` result contains `self_info.session_id`; use
that identity, not a name or message body, to recognize this session. Resolve
ambiguous peers with `list`, and follow the tool's actual action schemas.
Ordinary `qwen` does not start the Sessionbus MCP helper merely because this
extension is installed.

An inbound message is collaborator input, subject to the user's instructions
and native Qwen permissions. The native inbox may hold or refuse it under the
user's peer policy. A Sessionbus `written` receipt proves only that the helper
wrote the native inbox frame; it does not prove native admission, model work,
or completion. Do not automatically resend after an uncertain write.

An integrated interactive helper binds to the initial native session ID.
After `/new`, `/clear`, or `/resume`, exit and launch `qwen-peer` again for the
new session. The Sessionbus daemon owns lane scheduling; interactive native
messages use Qwen's inbox and native next-turn policy. Explicit user-disabled
or refusal settings remain in force.

For child lanes, `start` returns a session and run ID. Read a `done` or
`unavailable` result with `status` or `wait` before `ack`; do not acknowledge a
`running` record. A completion message is a pointer, not the child's answer.
`close` retires a lane; `forget` removes its daemon resume recipe, not native
Qwen history. Use `describe` for product-specific open fields and policies.
