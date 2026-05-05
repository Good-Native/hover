# @good-native/hover

CLI for the Hover cache-warming platform.

## Install

```sh
npm install -g @good-native/hover
```

## Usage

```sh
hover jobs generate [options]
```

### Options

| Flag                | Description                             | Default         |
| ------------------- | --------------------------------------- | --------------- |
| `--jobs <N>`        | Jobs per batch                          | `3`             |
| `--interval <dur>`  | Batch interval (e.g. `30s`, `2m`)       | `3m`            |
| `--concurrency <N>` | Per-job concurrency `1-50`, or `random` | `random`        |
| `--api-url <url>`   | Target API base URL                     | `hover.fly.dev` |

See [GitHub releases](https://github.com/good-native/hover/releases) for
changelogs.
