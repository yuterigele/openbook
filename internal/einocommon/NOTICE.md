# internal/einocommon — Vendored from cloudwego/eino-examples

These four packages are vendored from the upstream
[cloudwego/eino-examples](https://github.com/cloudwego/eino-examples)
monorepo so that `openbook` can build without depending on a local clone
of the sibling modules.

## What's here and where it came from

| This directory                                 | Upstream path                                          | License         |
| ---------------------------------------------- | ------------------------------------------------------ | --------------- |
| `internal/einocommon/store/`                   | `adk/common/store/`                                    | Apache-2.0      |
| `internal/einocommon/tool/`                    | `adk/common/tool/` (approval / follow_up / review_edit) | Apache-2.0      |
| `internal/einocommon/graphtool/`               | `adk/common/tool/graphtool/`                           | Apache-2.0      |
| `internal/einocommon/batch/`                   | `compose/batch/batch/`                                 | Apache-2.0      |

All four packages are licensed under Apache-2.0 (see LICENSE headers
in each file). The original copyright is held by CloudWeGo / ByteDance.

## When to sync

Sync these files when you intentionally bump `github.com/cloudwego/eino`
to a new minor version, OR when upstream ships a fix you actually need.
A short diff-and-merge is usually enough.

## Why not use `replace`?

We deliberately do NOT use a `replace` directive pointing to a local
clone of `cloudwego/eino-examples`. That pattern is fragile in CI and
makes the build non-reproducible on a fresh checkout. Vendoring keeps
the build self-contained.

## When to consider de-vendoring

If cloudwego ever publishes these as standalone Go modules with proper
semver tags (e.g. `github.com/cloudwego/eino-ext/...`), drop the
vendored copies and depend on them properly.
