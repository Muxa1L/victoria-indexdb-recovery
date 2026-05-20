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

func TestRecoverTreeRecoversStorageDataFiles(t *testing.T) {
	root := t.TempDir()
	partitionName := "202401"
	smallDir := filepath.Join(root, "data", storageSmallDirname, partitionName)
	bigDir := filepath.Join(root, "data", storageBigDirname, partitionName)
	if err := os.MkdirAll(smallDir, 0o755); err != nil {
		t.Fatalf("cannot create small partition dir: %s", err)
	}
	if err := os.MkdirAll(bigDir, 0o755); err != nil {
		t.Fatalf("cannot create big partition dir: %s", err)
	}

	partPath := filepath.Join(smallDir, "part-small")
	if err := os.MkdirAll(partPath, 0o755); err != nil {
		t.Fatalf("cannot create storage part dir: %s", err)
	}
	indexData := vmencoding.CompressZSTDLevel(nil, marshalTestStorageBlockHeader(storageBlockHeader{
		TSID: storageTSID{
			AccountID:     1,
			ProjectID:     2,
			MetricGroupID: 3,
			JobID:         4,
			InstanceID:    5,
			MetricID:      6,
		},
		MinTimestamp:          10,
		MaxTimestamp:          20,
		FirstValue:            30,
		TimestampsBlockOffset: 0,
		ValuesBlockOffset:     0,
		TimestampsBlockSize:   0,
		ValuesBlockSize:       0,
		RowsCount:             2,
		Scale:                 0,
		TimestampsMarshalType: storageMarshalType(vmencoding.MarshalTypeConst),
		ValuesMarshalType:     storageMarshalType(vmencoding.MarshalTypeConst),
		PrecisionBits:         64,
	}), 0)
	writeTestFile(t, filepath.Join(partPath, indexFilename), indexData)
	writeTestFile(t, filepath.Join(partPath, timestampsFilename), nil)
	writeTestFile(t, filepath.Join(partPath, valuesFilename), nil)
	writeTestFile(t, filepath.Join(partPath, "min_dedup_interval"), []byte("1s"))

	incompleteBigPart := filepath.Join(bigDir, "part-incomplete")
	if err := os.MkdirAll(incompleteBigPart, 0o755); err != nil {
		t.Fatalf("cannot create incomplete big part dir: %s", err)
	}
	writeTestFile(t, filepath.Join(incompleteBigPart, indexFilename), []byte("broken"))
	writeTestFile(t, filepath.Join(incompleteBigPart, timestampsFilename), nil)

	summary, err := recoverTree(root, false, false)
	if err != nil {
		t.Fatalf("recoverTree failed: %s", err)
	}
	if summary.metaindexFiles != 1 || summary.metadataFiles != 1 || summary.partsFiles != 1 {
		t.Fatalf("unexpected recovery summary: %+v", summary)
	}

	metaindexPath := filepath.Join(partPath, metaindexFilename)
	rows, err := readStorageMetaindex(metaindexPath)
	if err != nil {
		t.Fatalf("cannot read recovered storage metaindex: %s", err)
	}
	if len(rows) != 1 {
		t.Fatalf("unexpected storage metaindex row count: %d", len(rows))
	}
	if rows[0].TSID.MetricID != 6 || rows[0].BlockHeadersCount != 1 || rows[0].MinTimestamp != 10 || rows[0].MaxTimestamp != 20 {
		t.Fatalf("unexpected recovered storage metaindex row: %+v", rows[0])
	}

	metadataData := readTestFile(t, filepath.Join(partPath, metadataFilename))
	var metadata storagePartHeader
	if err := json.Unmarshal(metadataData, &metadata); err != nil {
		t.Fatalf("cannot parse recovered storage metadata: %s", err)
	}
	if metadata.RowsCount != 2 || metadata.BlocksCount != 1 || metadata.MinTimestamp != 10 || metadata.MaxTimestamp != 20 || metadata.MinDedupInterval != 1000 {
		t.Fatalf("unexpected recovered storage metadata: %+v", metadata)
	}

	partsData := readTestFile(t, filepath.Join(smallDir, partsFilename))
	var partNames storagePartNamesJSON
	if err := json.Unmarshal(partsData, &partNames); err != nil {
		t.Fatalf("cannot parse recovered storage parts.json: %s", err)
	}
	if len(partNames.Small) != 1 || partNames.Small[0] != "part-small" {
		t.Fatalf("unexpected small part names: %+v", partNames.Small)
	}
	if len(partNames.Big) != 0 {
		t.Fatalf("unexpected big part names: %+v", partNames.Big)
	}
}

func marshalTestStorageBlockHeader(bh storageBlockHeader) []byte {
	return bh.Marshal(nil)
}