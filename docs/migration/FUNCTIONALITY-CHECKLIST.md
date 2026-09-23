# Stable functionality checklist — Qwen separation

Baseline: original peers main `710e5d33369cba4fb9468cd24fea0fe844a0219d`.
F01–F20 are the shared migration requirement IDs. Extraction source preservation
and fresh installed behavior are separate evidence. Source extraction is
reviewed, PR #1's four hosted checks pass, and the permanent install is
complete and idempotent. Fresh default managed-idle wake remains incomplete;
the other wake surfaces remain untested on the extracted installation.
An uninstalled managed-lane candidate now hides only the Sessionbus Skill and
loads the same granted MCP tool directly; its effect on native model behavior
is untested.

| ID | Preserved functionality | Existing regression coverage | Installed evidence / limit |
|---|---|---|---|
| F01 | Complete archive install/update, extension and generic skill, private `qwen-peer-mcp` alias, checksum and archive-role safety | `package_archive_test.go`; release install/download/selection tests | Real-home install and idempotent reinstall of the exact `acd7c4c` archive passed; extension, plugin and aliases match the archive |
| F02 | One public binary, wrapper/native version and token-selected lane dispatch | `cmd/qwen-peer/main_test.go`; `version_test.go`; common peerversion | Permanent binary/source bind to `acd7c4c`; native Qwen 0.24.3 is observation provenance |
| F03 | Native argv order, literal `--`, group/name options, resume/continue/fork and `--yolo` | `arguments_test.go`; `interactive_config_test.go`; `interactive_name_test.go` | Preserve native selectors and title semantics; typed lane permission is separate |
| F04 | Default native policy plus exact managed `--allowed-tools mcp__sessionbus__sessionbus`; reject conflicting excludes | `arguments_test.go`; `review_config_test.go`; `lane_visibility_test.go` | Candidate: lane-only private `skills.disabled` hides `sessionbus:sessionbus` and same-name `--mcp-config` sets `alwaysLoadTools` on the already-granted MCP server; no Skill or unrelated grant, and the unmerged `5c1126e` remains excluded. Not installed or live-accepted |
| F05 | Ordinary Qwen stays ordinary: extension contains one skill, no global MCP manifest or helper | `package_test.go`; `package_archive_test.go` | Plain native launch exposes guidance only and no bus owner |
| F06 | Native identity/title, exclusive initial name claim, rename, resume and fork | `interactive_name_test.go`; `review_initial_name_test.go`; `interactive_reconnect_test.go` | Exact native/public identity join; preserve native history and session-switch limit |
| F07 | Discovery, list/send, public schema/errors and authenticated reply correlation | `peer_test.go`; `lane_endpoint_test.go`; common MCP tests | Direct MCP use historically works; ToolSearch may defer discovery |
| F08 | Zero-input lane open/readiness and native session new/resume | `qwen_test.go`; `acp_test.go`; `run_test.go` | Readiness before model input; no fabricated native terminal |
| F09 | Run/start/status/wait/ack and bounded result cursor | `run_test.go`; `qwen_test.go`; common lane tests | Collect actual native result before acknowledgement |
| F10 | Parent lifetime, notification and direct-child tracing | common host/MCP tests; public SDK | Authority and scheduling remain daemon-owned |
| F11 | Interactive idle inbound autonomous wake | `peer_test.go`; `interactive_events_test.go` | Untested on the extracted permanent install; one inbound, native reply/final and no later harness input remain to be shown |
| F12 | Interactive active admission during a native turn | `peer_test.go`; `interactive_events_test.go` | Untested on the extracted permanent install; preserve actual receipt and native turn chronology, not a substituted queued contract |
| F13 | Managed idle inbound starts one owned prompt | `run_test.go`; `delivery.go` source | QWK923A/B each admitted one inbound, then native 0.24.3 AUTO denied a model-selected, non-granted Skill before any MCP call, reply or final; installed wake incomplete |
| F14 | Managed active delivery is deferred to daemon scheduling; no blocked native mid-turn drain | `run_test.go`; `lane_endpoint_test.go` | Untested on the extracted permanent install; `queued_for_next_turn` is staging, not native consumption |
| F15 | Interactive reconnect, latest identity, supersession terminal | `interactive_reconnect_test.go`; `interactive_lifetime_test.go` | No replay or worker resurrection after terminal loss |
| F16 | Cancellation and protocol fidelity | `acp_test.go`; `forward_stdio_test.go`; `session_update_test.go` | Native and bus failure classes remain distinct |
| F17 | Startup, failure, close, native death and owned cleanup | `interactive_owner_test.go`; `interactive_registry_linux_test.go`; `interactive_registry_darwin_test.go` | Exact owned rows/processes absent; abrupt launcher death remains qualified |
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
  These results do not show whether native AUTO would permit direct selection
  of the granted MCP tool on 0.24.x. No bypass cell was run; bypass would not
  substitute for default mode.
- The source-only candidate preserves host system defaults and their schema
  version in a lane-private file, or fails Open before launch on an unsafe or
  unknown file. It preserves user/workspace settings and unrelated extensions,
  leaves AUTO and interactive launches untouched, and removes its private
  files after the ACP child exits; a panic or SIGKILL can leave them behind.
  The defaults-path variable is inherited by the lane child tree. Preservation
  is semantic, not byte-literal: JSON keys or escapes may change, but existing
  values and numbers keep their meaning. Managed Open fails closed on missing
  or nonliteral `$version: 4` (including `4.0`/`4e0`), files over 1 MiB, or
  unreadable, looping or non-directory paths. `--bare` or truthy inherited
  `QWEN_CODE_SIMPLE` with explicit `-e sessionbus` is rejected because the
  targeted skill hide would be ineffective. A model that still invokes the
  disabled Skill receives a native disabled-skill response, not a successful
  MCP send. The one-cell gate requires the actual granted MCP result, direct
  reply, exact final, and owned cleanup. None is claimed yet.
- One fresh cell gets one send, no replay, and no post-inbound harness prompt,
  native input or lifecycle turn. Preserve first failures and actual receipts.
- Native session switching remains limited to the launch's initial identity, as
  described in `qwen/README.md`; no hidden switch guard is claimed.
- Held lane skills under `docs/designs/qwen-0.5.0` are historical only, not
  shipped or activated. `measure.py` and `SIZE.json` are historical and cannot
  be reproduced after shared MCP extraction without changing their ownership
  accounting.

Protected-file/test normalization, source review, retained tests/race/vet/lint/
packaging and the exact permanent install are complete. Full behavioral
completion still requires fresh interactive and lane observations. QWK923A/B
remain incomplete wake evidence, and interactive idle/active and managed active
remain untested on the extracted installation.
