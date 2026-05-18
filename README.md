# indexdb-recovery

Standalone offline recovery tool for VictoriaMetrics mergeset indexdb parts.

It can rebuild:

- `metaindex.bin` from `index.bin`
- `metadata.json` from `index.bin`, `items.bin`, and `lens.bin`
- `parts.json` from the actual part directories

It cannot generally rebuild `lens.bin`, because `items.bin` does not store enough information to recover item boundaries without it.

Usage:

```bash
go run . -partsPath /path/to/indexdb
```

Useful flags:

- `-dryRun`
- `-force`
- `-verify`