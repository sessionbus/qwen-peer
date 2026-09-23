# Qwen extraction record

Baseline: `710e5d33369cba4fb9468cd24fea0fe844a0219d` from the complete
organisation copy. The original history and sibling repositories retain removed
product paths. The Qwen Go wrapper, native extension manifest, generic skill,
private `qwen-peer-mcp` alias, managed tool grant, native argument and permission
handling, install layout and archive recipe remain.

The frozen inventory accounts for 91 protected files and 146 original Go test
functions: 57 Qwen-owned files, the shared archive installer, one held Qwen
lane reference and 32 shared support files now resolved from peer-common. The
frozen record's phrase “held Grok lane reference” was a wording error: its
Qwen path and hash were correct. `PRESERVED-FILES.json` corrects only that
phrase and maps each file to its current location. Product runtime has no
intended behavior change.

Shared host/MCP/version/socket support and the legacy cleanup utility resolve to
`github.com/sessionbus/peer-common` at the reviewed immutable version
`v0.0.0-20260922143100-eb655f686e44` (commit
`eb655f686e4456a4c1121054763318e3d27e89b0`), without a filesystem
replacement or workspace. Linker flags stamp the Qwen release/revision into
the shared version helper. The legacy cleanup document now points to that
pinned source.

The root boundary, packaging, version and download tests and workflows are
scoped to Qwen. There is no ordinary `qwen/.mcp.json` or `qwen/mcp.json`:
ordinary native Qwen receives the skill only. Managed launches use the exact
Sessionbus tool grant and a per-session private alias. `qwen/plugin.json` keeps
its independent `0.4.0` extension identity. The `scripts/package-product qwen
DIR` interface and archive members, including the alias symlink, remain. The
archive installer retains the Qwen extension uninstall/install sequence and
native consent/user scope. The bootstrap retains checksum and role checks but
uses the canonical `sessionbus/qwen-peer` release URL. The packaged archive
needs no extra Node/npm runtime; the native Qwen application owns its runtime.

The Qwen design, acceptance record, facts, 0.23.0 native help and generic skill
remain. The held lane reference formerly under Claude design notes is copied
byte-for-byte to `docs/designs/qwen-0.5.0/legacy-lane-skill/SKILL.md`; it is
not packaged. Three deletion-induced Pi/OMP release-note links now point to
immutable original source in the Pi/OMP repository. The v0.5.0 README install
anchor follows this repository's README.

## Explicit exclusions and holds

The only unique unmerged change, signed commit
`5c1126e334349ab75072857da65a83d98249fcde`, adds a managed Skill grant
beside the directly granted MCP tool. It is deliberately **excluded**. Its
bundle, patch and signature evidence are preserved in
`/home/antst/sessionbus-evidence/qwen-preservation-inventory-opus-20260923/archive-5c1126e`;
the SHA256 of that archive's `SHA256SUMS` is
`c0d06ea8bf6b75bc68b2ca16ced679b14a70855bfbd0c8a83f33297e661f343d`.
UMKA currently runs that testing-only build; installing this baseline-derived
extraction reverts it. QWK922F still denied the model-selected Skill under AUTO
before MCP send, reply or final, so the commit is not a validated fix.

Direct MCP communication historically worked. QWK922E/F show that a
model-selected Skill-first wake may be denied under AUTO; the Skill is guidance,
not a prerequisite. Both are incomplete wake evidence, not product-wide
acceptance or proof that a direct MCP selection would fail. Any future Skill
grant change needs separate review and fresh installed evidence.

The organisation `qwen-peer` repository still has `develop` branch `183b8b5`,
and the binary-release workflow publishes on a push to develop. Actions remain
disabled; delete that branch or disable that workflow before enabling Actions.
The release and native extension versions are unchanged, and publication is
held. The original Codex repository's `install-qwen.sh`, pinned to final
combined v0.5.3 assets, remains the working public compatibility installer
until an independent Qwen release exists. The new bootstrap fails closed before
that release.

`measure.py` and `SIZE.json` are retained historical artifacts. Their former
local `wrappers/mcp` ownership accounting is not reproducible after the common
module split. Preserve the recorded values as history, not a fresh measurement.
Native Qwen versions are not pinned; exact versions/hashes identify observations,
not a compatibility allowlist.

Local source and archive verification does not replace a permanent real-home
install or fresh native acceptance. No UMKA install, model turn, config change,
tag or release is part of this extraction preparation.
