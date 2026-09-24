# Stable functionality checklist — Qwen separation

Baseline: original peers main `710e5d33369cba4fb9468cd24fea0fe844a0219d`.
F01–F20 are the shared migration requirement IDs. Extraction source preservation
and fresh installed behavior are separate evidence. Source extraction and the
interactive visibility implementation at `2ba3e12` are reviewed, and the
permanent `2ba3e12`/`0eb401e0` install is idempotent (binding `c145d755`).
Managed-idle QWK924R, managed-active QWQ924R and interactive-idle QWI924E
are independently reviewed clean original passes on that build. Interactive
active QWI925A remains an original FAIL caused by a harness projector defect,
with no native failure; QWI925B is an independently reviewed clean original
PASS for the tested steady-state lifecycle, so all four Qwen surfaces have
one accepted cell on this build.
QWK923A/B and QWI924A–D remain their original failures; QWI924A–D ran on
the earlier `bf6d0ea` build (observation `ca33703d`). Source completion,
installed binding and per-surface acceptance are separate conclusions.

| ID | Preserved functionality | Existing regression coverage | Installed evidence / limit |
|---|---|---|---|
| F01 | Complete archive install/update, extension and generic skill, private `qwen-peer-mcp` alias, checksum and archive-role safety | `package_archive_test.go`; release install/download/selection tests | The permanent `2ba3e12`/`0eb401e0` build replaced `bf6d0ea` with byte-identical post-install observations after two installs, exact inventory and binding `c145d755` (packet `6dfc48fd`); prior `acd7c4c` and `bf6d0ea` installs remain historical evidence |
| F02 | One public binary, wrapper/native version and token-selected lane dispatch | `cmd/qwen-peer/main_test.go`; `version_test.go`; common peerversion | Permanent binary/source bound to `2ba3e12`/`0eb401e0`; native Qwen 0.24.3 and observation `1909c885` are provenance, not a version allowlist |
| F03 | Native argv order, literal `--`, group/name options, resume/continue/fork and `--yolo` | `arguments_test.go`; `interactive_config_test.go`; `interactive_name_test.go`; `interactive_visibility_test.go` | Native selectors and title semantics remain; typed lane permission is separate. Installed compatibility restriction: integrated interactive launches reject explicit `-e sessionbus` combined with `--bare` or `--bare=true` before `--`, an exact `--bare` element after `--`, or truthy inherited `QWEN_CODE_SIMPLE`. Tokens after `--` remain in `argv._`, not `[query..]`; there only an exact `--bare` element enables bare mode. `--bare=x` and `--bare=TRUE` pass through unchanged, as does a single `"text --bare"` element after `--` |
| F04 | Default native policy plus exact managed `--allowed-tools mcp__sessionbus__sessionbus`; reject conflicting excludes | `arguments_test.go`; `review_config_test.go`; `lane_visibility_test.go`; `interactive_visibility_test.go` | By reviewed source/tests, the installed build supplies a private `skills.disabled:["sessionbus:sessionbus"]` defaults file and `alwaysLoadTools:true` Sessionbus MCP entry to managed and integrated interactive native children, preserving other settings, grants and native approval modes. QWI924E captured the effective child defaults and MCP entry; hiding is inferred from that capture (source-backed), since no native Skill listing was recoverable. Plain `qwen` is unchanged. Installed compatibility restriction: integrated interactive launch fails closed on unmergeable host system defaults, for example `$version` other than 4, JSONC, non-string `skills.disabled` entries, duplicate keys, trailing or invalid JSON, unreadable or non-regular files, symlink loops, directories, or files over 1 MiB. Native `.env` or settings `env` can enable bare mode after the wrapper's inherited-env check; this conditional residual remains for both surfaces. AUTO stayed enabled; unmerged `5c1126e` remains excluded |
| F05 | Ordinary Qwen stays ordinary: extension contains one skill, no global MCP manifest or helper | `package_test.go`; `package_archive_test.go` | Plain native launch exposes guidance only and no bus owner |
| F06 | Native identity/title, exclusive initial name claim, rename, resume and fork | `interactive_name_test.go`; `review_initial_name_test.go`; `interactive_reconnect_test.go` | Exact native/public identity join; preserve native history and session-switch limit |
| F07 | Discovery, list/send, public schema/errors and authenticated reply correlation | `peer_test.go`; `lane_endpoint_test.go`; common MCP tests | Integrated launches declare the granted Sessionbus MCP tool with `alwaysLoadTools:true`; successful informational `tool_search` calls may still occur, as in QWI925B, but its two searches selected no tool |
| F08 | Zero-input lane open/readiness and native session new/resume | `qwen_test.go`; `acp_test.go`; `run_test.go` | Readiness before model input; no fabricated native terminal |
| F09 | Run/start/status/wait/ack and bounded result cursor | `run_test.go`; `qwen_test.go`; common lane tests | Collect actual native result before acknowledgement |
| F10 | Parent lifetime, notification and direct-child tracing | common host/MCP tests; public SDK | Authority and scheduling remain daemon-owned |
| F11 | Interactive idle inbound autonomous wake | `peer_test.go`; `interactive_events_test.go`; `interactive_visibility_test.go` | QWI924E is an independently reviewed clean original PASS on `2ba3e12`: two ordinary inputs, tool-free setup, one direct explicitly successful MCP call joined to the operator-attested reply, exact final and owned cleanup under normal policy. QWI924A–D ran on `bf6d0ea` and remain original FAILs; root's later pinned native `tool_call` bridge criterion applies only to future cells and does not relabel C |
| F12 | Interactive active admission during a native turn | `peer_test.go`; `interactive_events_test.go` | QWI925A remains an original FAIL caused by a harness projector timestamp defect; raw native history showed no native failure. QWI925B is an independently reviewed clean original PASS (`6990b392`, review `bc41aa65`) on one observed mid-turn steer; interactive active is accepted for this steady-state lifecycle. The named fixture launched without `-i`, sent one harness-authored `input.jsonl` submit with zero PTY writes, and delivered the inbound only after title confirmation and accepted `session.hello`. For named launches, source-based analysis predicts that a send during an initial `-i` turn before publication is rejected as `unknown_session`; no live cell tested that timing. `written` plus the queued preview proves admission, not same-turn consumption. The gate accepts a mid-turn steer or next-turn delivery; one `mid_turn_user_message` steer was observed. Two successful informational `tool_search` calls selected nothing (an invented `mcp__plugin_qwen-code-dnd_…` name, then `sessionbus`) before the direct granted MCP call |
| F13 | Managed idle inbound starts one owned prompt | `run_test.go`; `delivery.go` source | QWK923C passed on `bf6d0ea`; QWK924R independently re-PASSed default AUTO managed idle on `2ba3e12`: exactly two ordinary inputs, tool-free setup, no injected rows, one successful granted MCP call, message-ID-joined, operator-attested direct reply (not a cryptographic receipt), exact final and owned cleanup. QWK923A/B remain original FAILs |
| F14 | Managed active delivery is deferred to daemon scheduling; no blocked native mid-turn drain | `run_test.go`; `lane_endpoint_test.go` | QWQ924C passed on `bf6d0ea`; QWQ924R independently re-PASSed default AUTO managed active on `2ba3e12`: queued receipt, blocked gate, single release, seeded Run, explicit MCP success, exact native final and owned cleanup |
| F15 | Interactive reconnect, latest identity, supersession terminal | `interactive_reconnect_test.go`; `interactive_lifetime_test.go` | No replay or worker resurrection after terminal loss |
| F16 | Cancellation and protocol fidelity | `acp_test.go`; `forward_stdio_test.go`; `session_update_test.go` | Native and bus failure classes remain distinct |
| F17 | Startup, failure, close, native death and owned cleanup | `interactive_owner_test.go`; `interactive_registry_linux_test.go`; `interactive_registry_darwin_test.go` | Exact owned rows/processes absent; abrupt launcher death remains qualified. Bounded follow-up: handle SIGHUP as owned termination and test removal of the one private launch directory and descendant behavior; current SIGHUP can leave its files behind |
| F18 | Native history, resume and independent auto-close policy | `interactive_events_test.go`; `review_recording_alias_test.go`; common lane tests | No wrapper history database or replay store |
| F19 | Independent module/archive/CI/release and platform builds | `architecture_test.go`; `version_test.go`; release tests | Exact source and archive on Linux/macOS amd64/arm64; publication held |
| F20 | Every original product/common runtime, asset, fixture and test accounted for | `PRESERVED-FILES.json` and baseline | 91 protected files and 146 original test functions resolve locally or in exact peer-common |

Coverage paths without a prefix are under `wrappers/qwen/`. Shared support is
pinned to peer-common `eb655f686e4456a4c1121054763318e3d27e89b0`; its 69
tests run in that repository. All Qwen-specific tests remain here. The test
count includes the original `TestMain` harness.

## Evidence rules and retained limitations

- Historical acceptance remains bound to its original binary and native version;
  it does not validate the extracted artifact. Native Qwen updates are expected;
  record actual version/hash as provenance, not an allowlist.
- Direct Sessionbus MCP historically worked in ordinary and bypass lanes and an
  ordinary peer. A model-selected `sessionbus:sessionbus` Skill-first wake was
  denied under AUTO in QWK922E/F before any MCP send, reply or final. The Skill
  is guidance, not a prerequisite or an approved substitute for the exact tool.
- Unique unmerged `5c1126e` is excluded. The permanent `acd7c4c` install
  replaced that testing-only build and passed an idempotent reinstall; dev2
  independently cleared the installed archive, extension and alias binding.
  The excluded commit's full signed history is
  preserved in `qwen-preservation-inventory-opus-20260923/archive-5c1126e`
  (SHA256 of archive `SHA256SUMS`:
  `c0d06ea8bf6b75bc68b2ca16ced679b14a70855bfbd0c8a83f33297e661f343d`).
- Fresh QWK923A and QWK923B used the installed default managed lane with only
  `mcp__sessionbus__sessionbus` granted. Both written inbound messages appear
  in native history, but the model selected the non-granted `skill` tool and
  native Qwen 0.24.3 AUTO denied it. Neither cell made an MCP call, sent a
  reply or emitted the requested wake final. A's denial also cited its old
  setup prohibition. B used the reviewed setup-only restraint; its denial no
  longer cited that prohibition. Both drivers later exited on local launcher
  stdin EOF, after the native AUTO denial and the model's next response;
  that EOF was a harness exit condition, not the Qwen outcome. Both original
  failures remain preserved.
  These A/B results alone did not show whether native AUTO would permit direct
  selection of the granted MCP tool on 0.24.x. No bypass cell was run; bypass
  would not substitute for default mode.
- By reviewed source and tests, the first installed managed option F build
  `bf6d0ea` preserves host
  system defaults and their schema version in a lane-private file, or fails
  Open before launch on an unsafe or unknown file. It preserves user/workspace
  settings and unrelated extensions, leaves AUTO and that build's interactive
  launches untouched, and removes its private files after the ACP child exits; a panic
  or SIGKILL can leave them behind.
  The defaults-path variable is inherited by the lane child tree. Preservation
  is semantic, not byte-literal: JSON keys or escapes may change, but existing
  values and numbers keep their meaning. Managed Open fails closed on missing
  or nonliteral `$version: 4` (including `4.0`/`4e0`), files over 1 MiB, or
  unreadable, looping or non-directory paths. `--bare` or truthy inherited
  `QWEN_CODE_SIMPLE` with explicit `-e sessionbus` is rejected because the
  targeted skill hide would be ineffective. A model that still invokes the
  disabled Skill receives a native disabled-skill response, not a successful
  MCP send. QWK923C exercised only the host-defaults-absent managed-idle path.
  It met the one-cell gate on default AUTO managed idle: the setup turn was
  tool-free; the final native history had exactly setup and inbound ordinary
  users, no injected rows under the empty environment profile, one explicitly
  successful granted MCP call and zero Skill, AUTO denial or `tool_search`
  calls. Its MCP result and operator-attested direct reply share the message
  ID; the reply is not a cryptographic receipt. The
  exact final, owned cleanup, during-run private defaults/MCP config and their
  removal after Close are retained in cell `qwen-lane-idle-qwk923c` (seal
  `53b93311`), with independent review in
  `qwen-f-review-records-opus-20260924` (seal `39448482`). Host system-defaults
  were observed absent at install (packet `239621ab`), not re-observed at cell
  time. The review record separately corrects the original outcome's
  process-scan wording; the original cell packet stays unchanged.
- The later reviewed `2ba3e12` source applies the targeted visibility merge
  to integrated interactive launches as well. The permanent real-home
  `2ba3e12`/`0eb401e0` install is bound by packet `6dfc48fd`, BINDING
  `c145d755`, and observation `1909c885`. Managed idle QWK924R (cell seal
  `c153b1e0`, independent review `edc29e93`) and managed active QWQ924R
  (`a890ca68`, review `c2d90c03`) independently re-PASSed under this build.
  Interactive idle QWI924E (`88d135f2`, review `0e53b88a`) independently
  PASSed with a direct successful MCP call and owned cleanup under normal
  policy. Its native-child capture records the private defaults and
  `alwaysLoadTools:true`; Skill hiding is inferred from that effective-state
  capture, source-backed, because no native Skill listing was directly
  recoverable. Host defaults were observed absent at install and in QWQ924R,
  QWI924E and QWI925A/B preflights; QWK924R did not re-observe absence at cell
  time, although its private defaults matched the fallback bytes. These
  observations do not test every merge or bare-mode configuration source.
  QWI924A–D ran on `bf6d0ea` and keep their original outcomes. Interactive
  active QWI925A is an original FAIL caused by a harness projector timestamp
  defect, with no native failure in the raw rows (classification `26f4fed5`);
  QWI925B is an independently reviewed clean original PASS (cell `6990b392`,
  review `bc41aa65`) on one mid-turn steer, so interactive active is accepted
  for that steady-state lifecycle. Its two successful informational
  `tool_search` calls selected no tools before the direct granted MCP call.
  All direct
  replies here are operator-attested and message-ID joined, not cryptographic
  receipts. QWI925A's original outcome is not relabelled.
- One fresh cell gets one send, no replay, and no post-inbound harness prompt,
  native input or lifecycle turn. Preserve first failures and actual receipts.
- Native session switching remains limited to the launch's initial identity, as
  described in `qwen/README.md`; no hidden switch guard is claimed.
- Held lane skills under `docs/designs/qwen-0.5.0` are historical only, not
  shipped or activated. `measure.py` and `SIZE.json` are historical and cannot
  be reproduced after shared MCP extraction without changing their ownership
  accounting.

Protected-file/test normalization, source review, retained tests/race/vet/lint/
packaging and the exact permanent install are complete. Both managed surfaces
re-PASSed on `2ba3e12`, and interactive idle PASSed once. Interactive active
has an independently reviewed clean original QWI925B PASS for the tested
steady-state lifecycle. All four Qwen surfaces now have an accepted cell on
`2ba3e12`. Earlier original failures remain preserved.
No tag, release or version changed.
