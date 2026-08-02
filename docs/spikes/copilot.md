# Official Copilot SDK Capability Spike

## Scope

This spike targets `github.com/github/copilot-sdk/go v1.0.8`, the official
upstream Go SDK pinned by r42. The tests use the public SDK against a local
in-memory-behavior TCP JSON-RPC runtime. They do not start the Copilot CLI, log
in, or contact an external service.

## Capability Matrix

| r42 requirement | v1.0.8 result | Evidence and assembly rule |
| --- | --- | --- |
| Model and arbitrary reasoning effort | Supported | `SessionConfig.Model` and `ReasoningEffort` are strings. `CreateSession` forwards them without Go-side enum validation. The probe sends `max` and observes it unchanged. |
| Provider endpoint and protocol | Supported | `ProviderConfig` exposes provider type, wire API, transport, base URL, API key, bearer token, Azure options, and headers. r42 resolves `*_ref` environment names before constructing this value. |
| Appended system prompt | Supported | `SystemMessageConfig{Mode: "append"}` preserves the SDK foundation and appends r42 plus author instructions. |
| Typed custom tools | Supported | `DefineTool[T, U]` generates JSON Schema, decodes JSON arguments into `T`, invokes the handler, and serializes non-string `U` as JSON. `Tool.Name` is preserved exactly. |
| Streaming assistant, reasoning, tool, and usage events | Supported | r42 sets `SessionConfig.Streaming = true`, subscribes to message/reasoning deltas, tool execution lifecycle events, and assistant usage, and preserves the raw SDK payload in debug mode. Reasoning content is available only when the provider emits it. |
| Exact-name dispatch | Supported | A session registers handlers in a map keyed by `Tool.Name`; incoming calls look up `ToolName` exactly. r42 can therefore use names such as `go_tool_finish`. |
| Allow and deny filters | Supported | Both lists are forwarded. v1.0.8 sets `toolFilterPrecedence` to `excluded`, so deny wins when both contain a tool. Public field comments that say allow wins are stale. |
| `approve_all` | Supported | `PermissionHandler.ApproveAll` returns `PermissionDecisionApproveOnce` for every request, which automatically approves each otherwise valid call without persisting a broader permission. |
| Skill roots, selected skills, and disabled skills | Supported | Roots use `SessionConfig.SkillDirectories`; disabled names use `DisabledSkills`. Selected names require `CustomAgentConfig.Skills` plus `SessionConfig.Agent`. |
| Persistent multi-turn session | Supported | Repeated `Session.Send` calls use the same session ID. A new SDK session is not created for each turn. |
| Close one logical session | Supported with naming/lifecycle caveats | The API is `Session.Disconnect()`, not `CloseSession`. It sends `session.destroy`, clears in-memory handlers, and preserves resumable on-disk state, but it does not unregister itself from the client. |

The executable probe additionally locks the real `session.create`,
`session.send`, and `session.destroy` wire fields through the public
`URIConnection` transport. SDK source inspection confirms the exact handler
map lookup and the SDK-requested excluded-filter wire precedence represented by
those fields. The fake runtime records that request; SDK source and protocol
inspection supply the deny-wins semantic evidence rather than the fake applying
filters itself.

## Session Assembly Consequences

Selected skills are not a direct `SessionConfig.Skills` field. r42 must create
and activate a custom agent whose `Skills` contains the configured names. The
custom agent needs a prompt. Its model falls back to the parent session model,
but reasoning effort explicitly does not inherit, so r42 must copy the
effective model and reasoning effort into that custom agent. The probe locks
this shape.

The SDK accepts raw `Tool.Name` values independently of its optional `ToolSet`
builder. r42 therefore registers each typed tool under its deterministic,
SDK-safe `tool_<kind>_<name>_<uuid>` ID. Mandatory terminate and QC tools remain
an r42 assembly concern: r42 must register them and keep them out of the
effective deny set.

## Unsupported Or r42-Owned Behavior

- The SDK exposes no retry policy or transient-error classifier for lifecycle or
  model calls. r42 owns those policies.
- There is no terminal-tool, required-tool, protocol-attempt, or block-timeout
  concept in the SDK.
- There is no session-level selected-skills field; selection uses an activated
  custom agent as described above.
- There is no method named `CloseSession`. `Disconnect` has no context parameter
  and internally uses `context.Background()` for `session.destroy`, so a block
  deadline cannot directly cancel that cleanup RPC.
- A successful `Disconnect` does not remove the session from the client's
  registry. A later `Client.Stop()` sends `session.destroy` for that session
  again; the probe demonstrates both requests. r42 must account for this
  non-idempotent RPC lifecycle when it reports cleanup errors.
- `Client.Stop()` disconnects every tracked session and is not the per-block
  close operation. `Client.DeleteSession()` permanently removes persisted state
  and must not be used as close.
- The Go layer forwards provider combinations and arbitrary reasoning values;
  provider/runtime rejection remains an Apply-time error.

Unsupported optional SDK fields are not emulated by this spike.
