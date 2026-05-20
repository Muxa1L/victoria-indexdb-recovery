package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	vmencoding "github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
)

func TestRecoverTreeDryRunDoesNotModifyFiles(t *testing.T) {
	root := t.TempDir()
	partPath := filepath.Join(root, "index", "part-0001")
	if err := os.MkdirAll(partPath, 0o755); err != nil {
		t.Fatalf("cannot create test part dir: %s", err)
	}

	firstItem := []byte("metric.name")
	indexData := vmencoding.CompressZSTDLevel(nil, marshalTestBlockHeader(firstItem), 0)
	itemsData := []byte{}
	lensData := []byte{}

	writeTestFile(t, filepath.Join(partPath, indexFilename), indexData)
	writeTestFile(t, filepath.Join(partPath, itemsFilename), itemsData)
	writeTestFile(t, filepath.Join(partPath, lensFilename), lensData)

	beforeIndex := append([]byte{}, indexData...)
	beforeItems := append([]byte{}, itemsData...)
	beforeLens := append([]byte{}, lensData...)

	summary, err := recoverTree(root, true, false)
	if err != nil {
		t.Fatalf("recoverTree dry run failed: %s", err)
	}
	if summary.metaindexFiles != 1 || summary.metadataFiles != 1 || summary.partsFiles != 1 {
		t.Fatalf("unexpected dry-run summary: %+v", summary)
	}

	assertMissing(t, filepath.Join(partPath, metaindexFilename))
	assertMissing(t, filepath.Join(partPath, metadataFilename))
	assertMissing(t, filepath.Join(root, "index", partsFilename))

	afterIndex := readTestFile(t, filepath.Join(partPath, indexFilename))
	afterItems := readTestFile(t, filepath.Join(partPath, itemsFilename))
	afterLens := readTestFile(t, filepath.Join(partPath, lensFilename))

	if !bytes.Equal(afterIndex, beforeIndex) {
		t.Fatalf("index.bin changed during dry run")
	}
	if !bytes.Equal(afterItems, beforeItems) {
		t.Fatalf("items.bin changed during dry run")
	}
	if !bytes.Equal(afterLens, beforeLens) {
		t.Fatalf("lens.bin changed during dry run")
	}
}

func marshalTestBlockHeader(firstItem []byte) []byte {
	var dst []byte
	dst = vmencoding.MarshalBytes(dst, nil)
	dst = vmencoding.MarshalBytes(dst, firstItem)
	dst = append(dst, byte(marshalTypePlain))
	dst = vmencoding.MarshalUint32(dst, 1)
	dst = vmencoding.MarshalUint64(dst, 0)
	dst = vmencoding.MarshalUint64(dst, 0)
	dst = vmencoding.MarshalUint32(dst, 0)
	dst = vmencoding.MarshalUint32(dst, 0)
	return dst
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("cannot write %q: %s", path, err)
	}
}

func readTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %q: %s", path, err)
	}
	return data
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %q to be missing after dry run; got err=%v", path, err)
	}
}

func TestHexStringUnmarshalJSON(t *testing.T) {
	input := []byte(`{"ItemsCount":1,"BlocksCount":1,"FirstItem":"0102aa","LastItem":"ff00"}`)
	var ph partHeader
	if err := json.Unmarshal(input, &ph); err != nil {
		t.Fatalf("cannot unmarshal metadata JSON: %s", err)
	}
	if !bytes.Equal(ph.FirstItem, []byte{0x01, 0x02, 0xaa}) {
		t.Fatalf("unexpected FirstItem; got %x", []byte(ph.FirstItem))
	}
	if !bytes.Equal(ph.LastItem, []byte{0xff, 0x00}) {
		t.Fatalf("unexpected LastItem; got %x", []byte(ph.LastItem))
	}
}

func TestBuildPartsFileDataSkipsIncompleteParts(t *testing.T) {
	root := t.TempDir()
	indexDir := filepath.Join(root, "index")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("cannot create index dir: %s", err)
	}

	completePart := filepath.Join(indexDir, "part-complete")
	if err := os.MkdirAll(completePart, 0o755); err != nil {
		t.Fatalf("cannot create complete part dir: %s", err)
	}
	writeTestFile(t, filepath.Join(completePart, indexFilename), []byte("index"))
	writeTestFile(t, filepath.Join(completePart, itemsFilename), []byte("items"))
	writeTestFile(t, filepath.Join(completePart, lensFilename), []byte("lens"))

	incompletePart := filepath.Join(indexDir, "part-incomplete")
	if err := os.MkdirAll(incompletePart, 0o755); err != nil {
		t.Fatalf("cannot create incomplete part dir: %s", err)
	}
	writeTestFile(t, filepath.Join(incompletePart, indexFilename), []byte("index"))
	writeTestFile(t, filepath.Join(incompletePart, itemsFilename), []byte("items"))

	data, err := buildPartsFileData(indexDir)
	if err != nil {
		t.Fatalf("cannot build parts.json data: %s", err)
	}
	var partNames []string
	if err := json.Unmarshal(data, &partNames); err != nil {
		t.Fatalf("cannot parse generated parts.json data: %s", err)
	}
	if len(partNames) != 1 || partNames[0] != "part-complete" {
		t.Fatalf("unexpected part names; got %v; want [part-complete]", partNames)
	}
}