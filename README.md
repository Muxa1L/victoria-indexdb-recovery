# indexdb-recovery

Standalone offline recovery tool for VictoriaMetrics mergeset indexdb parts.

It can rebuild:

- `metaindex.bin` from `index.bin`
- `metadata.json` from `index.bin`, `items.bin`, and `lens.bin`
- `parts.json` from the actual part directories

If a part cannot be scanned during recovery, the tool moves that part directory under a sibling `unrecoverable/` directory and continues processing the remaining parts. Those quarantined directories are excluded from regenerated `parts.json` files.

It cannot generally rebuild `lens.bin`, because `items.bin` does not store enough information to recover item boundaries without it.

Usage:

```bash
go run . -partsPath /path/to/indexdb
```

Useful flags:

- `-partsPath`: root directory to scan recursively for mergeset parts and parent `parts.json` files.
- `-dryRun`: prints the files that would be rebuilt and does not write any changes.
- `-force`: rewrites `metadata.json` and `metaindex.bin` even when those files already exist.
- `-verify`: checks whether `metaindex.bin`, `metadata.json`, and `parts.json` already match the recoverable state and exits with a non-zero status if mismatches are found.
- `-readChunkSize`: size in bytes for cached chunked reads while scanning `index.bin`; the default is `1048576` (1 MiB). Larger values can improve throughput on slower storage, while smaller values reduce RAM per active scanner.