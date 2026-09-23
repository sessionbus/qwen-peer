# Sessionbus Qwen peer

Connect native Qwen sessions through [Sessionbus](https://github.com/sessionbus/sessionbus).
`qwen-peer` provides interactive peers and managed lanes from one Go binary.
It preserves native Qwen tools, permissions and history.

## Install

Install native Qwen and Sessionbus first using your normal home, login and PATH.

```sh
curl -fsSL https://raw.githubusercontent.com/sessionbus/qwen-peer/main/scripts/install-qwen.sh | sh
```

This repository is being separated from the original peers tree. No independent
release is published yet; use a reviewed archive built from source until release.
The installer retains checksum verification, archive-role checks and latest
stable/development selection against this repository; until a release exists it
stops without installing and never fetches another product. Older published
installer links remain compatibility entrypoints in
[the original repository](https://github.com/sessionbus/codex-peer), pinned to the
final combined v0.5.3 assets.

See [the Qwen guide](qwen/README.md) for the permanent install/update procedure,
the private `qwen-peer-mcp` alias, native flags, the managed tool grant, resume,
identity, and lane lifecycle behavior. The archive contains the Go executable,
its private alias and Qwen extension with one generic skill. The native Qwen
installation supplies its own Node runtime; this package adds no Node adapter,
npm dependency, or separate Node installation.

Ordinary Qwen discovers the skill only: the extension has no global MCP manifest.
An interactive `qwen-peer` launch adds a per-launch private MCP configuration;
a managed lane supplies its own per-session MCP configuration. The managed
native grant is exactly `--allowed-tools mcp__sessionbus__sessionbus`. The
private alias dispatches through the same installed binary and never broadens
native permission policy.

## Updating older installations

`list` reports the bound originating caller in `self_info` alongside the visible
`sessions`. Compare its `session_id` with row IDs to recognize self; a filter or
remote host query does not change the caller identity. Older daemons may omit
this field, which must not be guessed from names or row order.

When updating an existing installation for `self_info`, update every product
peer first and restart managed sessions so their helpers load the updated SDK.
Then update the host daemon. Older SDKs reject the new response field; updated
SDKs also accept older daemon responses. Coordinate federated host upgrades as
well, since daemons validate forwarded responses with their embedded SDK.

## Build and test

```sh
git clone https://github.com/sessionbus/qwen-peer.git
cd qwen-peer
GOWORK=off go mod download
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
scripts/package-product qwen ./dist
```

Builds require Go 1.24 or newer; target installations do not need Go. Packaging
supports Linux/macOS amd64/arm64. This extraction does not bump RELEASE_VERSION
or the Qwen extension manifest's independent `0.4.0` identity. Publication
remains held during validation. Shared support uses the exact peer-common
version/checksum in go.mod/go.sum. Native Qwen versions are not pinned: observed
versions and hashes identify test evidence, not a runtime allowlist.

The [stable functionality checklist](docs/migration/FUNCTIONALITY-CHECKLIST.md)
and [preservation inventory](docs/migration/PRESERVED-FILES.json) track separation.
Historical behavior and limits remain in [Qwen facts](docs/products/qwen.md)
and the [Qwen design and acceptance records](docs/designs/qwen-0.5.0/ACCEPTANCE.md).
The held lane skill remains documentation only and is not packaged or activated.
Fresh extracted-build validation remains pending.

## Version reporting

`qwen-peer --version` and `qwen-peer -v` report the peer release and exact source
revision without starting native Qwen. Use exact `--native-version` to request
native Qwen's own `--version` outside lane mode; lane workers reject that escape.
Development builds print `development` with their VCS revision. A stable build
fails before packaging unless its `vX.Y.Z` tag and [`RELEASE_VERSION`](RELEASE_VERSION)
agree.

## Native arguments and delivery

Native initial selectors, including resume, continue and fork, remain native
arguments. Wrapper `-g`/`--group` and `-n`/`--name`/`--peer-name` options are
parsed before literal `--`; later tokens pass through unchanged. A typed lane
`permission_mode` remains distinct from native interactive flags. A caller's
`--exclude-tools` rule may not defeat the managed Sessionbus tool grant.

A delivery reported as `rejected` with reason `no_receipt` means that no usable
receipt was obtained. It does not prove the message was never submitted or
consumed. Preserve its identity and do not replay automatically. `connected`
describes bus attachment, and `running` describes a managed Run; neither alone
proves an interactive Qwen model is idle. Follow the actual receipt.

The tool's installed declaration governs `{action, arguments}`. Use `describe`
for `qwen-peer` Open fields. The generic skill explains collection, acknowledgment
and independent lifetime policies. Native client versions may change without a
wrapper release; test concrete behavior and record exact provenance.
