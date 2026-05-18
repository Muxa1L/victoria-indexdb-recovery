package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

var (
	partsPath = flag.String("partsPath", "", "Path to indexdb directory or another directory containing mergeset parts")
	dryRun    = flag.Bool("dryRun", false, "List files that would be rebuilt without writing them")
	force     = flag.Bool("force", false, "Rebuild metadata.json and metaindex.bin even if they already exist")
	verify    = flag.Bool("verify", false, "Verify that metaindex.bin, metadata.json, and parts.json match the recoverable on-disk state without rewriting them")
)

func main() {
	flag.Parse()
	if *partsPath == "" {
		fmt.Fprintln(os.Stderr, "missing -partsPath")
		os.Exit(2)
	}

	root := filepath.Clean(*partsPath)
	info, err := os.Stat(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot stat %q: %s\n", root, err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Fprintf(os.Stderr, "%q must be a directory\n", root)
		os.Exit(1)
	}

	if *verify {
		summary, err := verifyTree(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot verify indexdb files under %q: %s\n", root, err)
			os.Exit(1)
		}
		fmt.Printf("checked %d metaindex.bin, %d metadata.json, and %d parts.json file(s) under %s\n",
			summary.metaindexFiles, summary.metadataFiles, summary.partsFiles, root)
		if summary.mismatches > 0 {
			fmt.Fprintf(os.Stderr, "detected %d mismatch(es)\n", summary.mismatches)
			os.Exit(1)
		}
		return
	}

	summary, err := recoverTree(root, *dryRun, *force)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot recover indexdb files under %q: %s\n", root, err)
		os.Exit(1)
	}

	verb := "rebuilt"
	if *dryRun {
		verb = "would rebuild"
	}
	fmt.Printf("%s %d metaindex.bin, %d metadata.json, and %d parts.json file(s) under %s\n",
		verb, summary.metaindexFiles, summary.metadataFiles, summary.partsFiles, root)
}