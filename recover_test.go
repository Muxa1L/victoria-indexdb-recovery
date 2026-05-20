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

func TestRecoverTreeDryRunDoesNotModifyStorageFiles(t *testing.T) {
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
	timestampsData := []byte("timestamps")
	valuesData := []byte("values")
	minDedupData := []byte("1s")

	writeTestFile(t, filepath.Join(partPath, indexFilename), indexData)
	writeTestFile(t, filepath.Join(partPath, timestampsFilename), timestampsData)
	writeTestFile(t, filepath.Join(partPath, valuesFilename), valuesData)
	writeTestFile(t, filepath.Join(partPath, "min_dedup_interval"), minDedupData)

	beforeIndex := append([]byte{}, indexData...)
	beforeTimestamps := append([]byte{}, timestampsData...)
	beforeValues := append([]byte{}, valuesData...)
	beforeMinDedup := append([]byte{}, minDedupData...)

	summary, err := recoverTree(root, true, false)
	if err != nil {
		t.Fatalf("recoverTree storage dry run failed: %s", err)
	}
	if summary.metaindexFiles != 1 || summary.metadataFiles != 1 || summary.partsFiles != 1 {
		t.Fatalf("unexpected storage dry-run summary: %+v", summary)
	}

	assertMissing(t, filepath.Join(partPath, metaindexFilename))
	assertMissing(t, filepath.Join(partPath, metadataFilename))
	assertMissing(t, filepath.Join(smallDir, partsFilename))

	afterIndex := readTestFile(t, filepath.Join(partPath, indexFilename))
	afterTimestamps := readTestFile(t, filepath.Join(partPath, timestampsFilename))
	afterValues := readTestFile(t, filepath.Join(partPath, valuesFilename))
	afterMinDedup := readTestFile(t, filepath.Join(partPath, "min_dedup_interval"))

	if !bytes.Equal(afterIndex, beforeIndex) {
		t.Fatalf("storage index.bin changed during dry run")
	}
	if !bytes.Equal(afterTimestamps, beforeTimestamps) {
		t.Fatalf("timestamps.bin changed during dry run")
	}
	if !bytes.Equal(afterValues, beforeValues) {
		t.Fatalf("values.bin changed during dry run")
	}
	if !bytes.Equal(afterMinDedup, beforeMinDedup) {
		t.Fatalf("min_dedup_interval changed during dry run")
	}
	if _, err := os.Stat(filepath.Join(bigDir, partsFilename)); !os.IsNotExist(err) {
		t.Fatalf("unexpected parts.json under big partition dir; err=%v", err)
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

func TestBuildStoragePartsFileDataSkipsIncompleteParts(t *testing.T) {
	root := t.TempDir()
	smallDir := filepath.Join(root, "data", storageSmallDirname, "202401")
	bigDir := filepath.Join(root, "data", storageBigDirname, "202401")
	if err := os.MkdirAll(smallDir, 0o755); err != nil {
		t.Fatalf("cannot create small partition dir: %s", err)
	}
	if err := os.MkdirAll(bigDir, 0o755); err != nil {
		t.Fatalf("cannot create big partition dir: %s", err)
	}

	completeSmallPart := filepath.Join(smallDir, "part-complete")
	if err := os.MkdirAll(completeSmallPart, 0o755); err != nil {
		t.Fatalf("cannot create complete small part: %s", err)
	}
	for _, name := range []string{indexFilename, timestampsFilename, valuesFilename, metaindexFilename, metadataFilename} {
		writeTestFile(t, filepath.Join(completeSmallPart, name), []byte(name))
	}

	missingMetaindexPart := filepath.Join(smallDir, "part-no-metaindex")
	if err := os.MkdirAll(missingMetaindexPart, 0o755); err != nil {
		t.Fatalf("cannot create incomplete small part: %s", err)
	}
	for _, name := range []string{indexFilename, timestampsFilename, valuesFilename, metadataFilename} {
		writeTestFile(t, filepath.Join(missingMetaindexPart, name), []byte(name))
	}

	missingMetadataPart := filepath.Join(bigDir, "part-no-metadata")
	if err := os.MkdirAll(missingMetadataPart, 0o755); err != nil {
		t.Fatalf("cannot create incomplete big part: %s", err)
	}
	for _, name := range []string{indexFilename, timestampsFilename, valuesFilename, metaindexFilename} {
		writeTestFile(t, filepath.Join(missingMetadataPart, name), []byte(name))
	}

	data, err := buildStoragePartsFileData(smallDir, bigDir)
	if err != nil {
		t.Fatalf("cannot build storage parts.json data: %s", err)
	}
	var partNames storagePartNamesJSON
	if err := json.Unmarshal(data, &partNames); err != nil {
		t.Fatalf("cannot parse generated storage parts.json data: %s", err)
	}
	if len(partNames.Small) != 1 || partNames.Small[0] != "part-complete" {
		t.Fatalf("unexpected small part names; got %v; want [part-complete]", partNames.Small)
	}
	if len(partNames.Big) != 0 {
		t.Fatalf("unexpected big part names; got %v; want []", partNames.Big)
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

func TestRecoverTreeMovesUnrecoverableMergesetParts(t *testing.T) {
	root := t.TempDir()
	partitionName := "202401"
	indexDir := filepath.Join(root, "data", "indexdb", partitionName)
	goodPart := filepath.Join(indexDir, "part-good")
	badPart := filepath.Join(indexDir, "part-bad")
	for _, partPath := range []string{goodPart, badPart} {
		if err := os.MkdirAll(partPath, 0o755); err != nil {
			t.Fatalf("cannot create test part dir %q: %s", partPath, err)
		}
	}

	goodIndexData := vmencoding.CompressZSTDLevel(nil, marshalTestBlockHeader([]byte("metric.name")), 0)
	writeTestFile(t, filepath.Join(goodPart, indexFilename), goodIndexData)
	writeTestFile(t, filepath.Join(goodPart, itemsFilename), nil)
	writeTestFile(t, filepath.Join(goodPart, lensFilename), nil)

	writeTestFile(t, filepath.Join(badPart, indexFilename), []byte("broken"))
	writeTestFile(t, filepath.Join(badPart, itemsFilename), nil)
	writeTestFile(t, filepath.Join(badPart, lensFilename), nil)

	summary, err := recoverTree(root, false, false)
	if err != nil {
		t.Fatalf("recoverTree failed: %s", err)
	}
	if summary.metaindexFiles != 1 || summary.metadataFiles != 1 || summary.partsFiles != 1 {
		t.Fatalf("unexpected recovery summary: %+v", summary)
	}

	assertMissing(t, badPart)
	quarantinedBadPart := filepath.Join(root, "data", brokenPartsDirname, "indexdb", partitionName, "part-bad")
	if _, err := os.Stat(quarantinedBadPart); err != nil {
		t.Fatalf("expected quarantined part at %q: %s", quarantinedBadPart, err)
	}

	partsData := readTestFile(t, filepath.Join(indexDir, partsFilename))
	var partNames []string
	if err := json.Unmarshal(partsData, &partNames); err != nil {
		t.Fatalf("cannot parse recovered parts.json: %s", err)
	}
	if len(partNames) != 1 || partNames[0] != "part-good" {
		t.Fatalf("unexpected part names after quarantine: %+v", partNames)
	}
}

func TestRecoverTreeMovesUnrecoverableStorageParts(t *testing.T) {
	root := t.TempDir()
	partitionName := "202401"
	smallDir := filepath.Join(root, "data", storageSmallDirname, partitionName)
	if err := os.MkdirAll(smallDir, 0o755); err != nil {
		t.Fatalf("cannot create small partition dir: %s", err)
	}

	goodPart := filepath.Join(smallDir, "part-good")
	badPart := filepath.Join(smallDir, "part-bad")
	for _, partPath := range []string{goodPart, badPart} {
		if err := os.MkdirAll(partPath, 0o755); err != nil {
			t.Fatalf("cannot create storage part dir %q: %s", partPath, err)
		}
	}

	goodIndexData := vmencoding.CompressZSTDLevel(nil, marshalTestStorageBlockHeader(storageBlockHeader{
		TSID: storageTSID{AccountID: 1, ProjectID: 2, MetricGroupID: 3, JobID: 4, InstanceID: 5, MetricID: 6},
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
	writeTestFile(t, filepath.Join(goodPart, indexFilename), goodIndexData)
	writeTestFile(t, filepath.Join(goodPart, timestampsFilename), nil)
	writeTestFile(t, filepath.Join(goodPart, valuesFilename), nil)

	writeTestFile(t, filepath.Join(badPart, indexFilename), []byte("broken"))
	writeTestFile(t, filepath.Join(badPart, timestampsFilename), nil)
	writeTestFile(t, filepath.Join(badPart, valuesFilename), nil)

	summary, err := recoverTree(root, false, false)
	if err != nil {
		t.Fatalf("recoverTree failed: %s", err)
	}
	if summary.metaindexFiles != 1 || summary.metadataFiles != 1 || summary.partsFiles != 1 {
		t.Fatalf("unexpected recovery summary: %+v", summary)
	}

	assertMissing(t, badPart)
	quarantinedBadPart := filepath.Join(root, "data", brokenPartsDirname, storageSmallDirname, partitionName, "part-bad")
	if _, err := os.Stat(quarantinedBadPart); err != nil {
		t.Fatalf("expected quarantined storage part at %q: %s", quarantinedBadPart, err)
	}

	partsData := readTestFile(t, filepath.Join(smallDir, partsFilename))
	var partNames storagePartNamesJSON
	if err := json.Unmarshal(partsData, &partNames); err != nil {
		t.Fatalf("cannot parse recovered storage parts.json: %s", err)
	}
	if len(partNames.Small) != 1 || partNames.Small[0] != "part-good" {
		t.Fatalf("unexpected storage part names after quarantine: %+v", partNames.Small)
	}
}

func TestRecoverTreeMovesUnrecoverableBigStorageParts(t *testing.T) {
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

	goodPart := filepath.Join(smallDir, "part-good")
	badPart := filepath.Join(bigDir, "part-bad")
	if err := os.MkdirAll(goodPart, 0o755); err != nil {
		t.Fatalf("cannot create storage part dir %q: %s", goodPart, err)
	}
	if err := os.MkdirAll(badPart, 0o755); err != nil {
		t.Fatalf("cannot create storage part dir %q: %s", badPart, err)
	}

	goodIndexData := vmencoding.CompressZSTDLevel(nil, marshalTestStorageBlockHeader(storageBlockHeader{
		TSID: storageTSID{AccountID: 1, ProjectID: 2, MetricGroupID: 3, JobID: 4, InstanceID: 5, MetricID: 6},
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
	writeTestFile(t, filepath.Join(goodPart, indexFilename), goodIndexData)
	writeTestFile(t, filepath.Join(goodPart, timestampsFilename), nil)
	writeTestFile(t, filepath.Join(goodPart, valuesFilename), nil)

	writeTestFile(t, filepath.Join(badPart, indexFilename), []byte("broken"))
	writeTestFile(t, filepath.Join(badPart, timestampsFilename), nil)
	writeTestFile(t, filepath.Join(badPart, valuesFilename), nil)

	summary, err := recoverTree(root, false, false)
	if err != nil {
		t.Fatalf("recoverTree failed: %s", err)
	}
	if summary.metaindexFiles != 1 || summary.metadataFiles != 1 || summary.partsFiles != 1 {
		t.Fatalf("unexpected recovery summary: %+v", summary)
	}

	assertMissing(t, badPart)
	quarantinedBadPart := filepath.Join(root, "data", brokenPartsDirname, storageBigDirname, partitionName, "part-bad")
	if _, err := os.Stat(quarantinedBadPart); err != nil {
		t.Fatalf("expected quarantined big storage part at %q: %s", quarantinedBadPart, err)
	}

	partsData := readTestFile(t, filepath.Join(smallDir, partsFilename))
	var partNames storagePartNamesJSON
	if err := json.Unmarshal(partsData, &partNames); err != nil {
		t.Fatalf("cannot parse recovered storage parts.json: %s", err)
	}
	if len(partNames.Small) != 1 || partNames.Small[0] != "part-good" {
		t.Fatalf("unexpected small storage part names after big quarantine: %+v", partNames.Small)
	}
	if len(partNames.Big) != 0 {
		t.Fatalf("unexpected big storage part names after quarantine: %+v", partNames.Big)
	}
}

func marshalTestStorageBlockHeader(bh storageBlockHeader) []byte {
	return bh.Marshal(nil)
}

func TestUnmarshalStorageBlockHeadersAllowsSameTSIDOutOfTimestampOrder(t *testing.T) {
	data := append(
		marshalTestStorageBlockHeader(storageBlockHeader{
			TSID: storageTSID{
				AccountID:     1,
				ProjectID:     2,
				MetricGroupID: 3,
				JobID:         4,
				InstanceID:    5,
				MetricID:      6,
			},
			MinTimestamp:          20,
			MaxTimestamp:          30,
			FirstValue:            1,
			TimestampsBlockOffset: 0,
			ValuesBlockOffset:     0,
			TimestampsBlockSize:   0,
			ValuesBlockSize:       0,
			RowsCount:             1,
			Scale:                 0,
			TimestampsMarshalType: storageMarshalType(vmencoding.MarshalTypeConst),
			ValuesMarshalType:     storageMarshalType(vmencoding.MarshalTypeConst),
			PrecisionBits:         64,
		}),
		marshalTestStorageBlockHeader(storageBlockHeader{
			TSID: storageTSID{
				AccountID:     1,
				ProjectID:     2,
				MetricGroupID: 3,
				JobID:         4,
				InstanceID:    5,
				MetricID:      6,
			},
			MinTimestamp:          10,
			MaxTimestamp:          19,
			FirstValue:            2,
			TimestampsBlockOffset: 10,
			ValuesBlockOffset:     10,
			TimestampsBlockSize:   0,
			ValuesBlockSize:       0,
			RowsCount:             1,
			Scale:                 0,
			TimestampsMarshalType: storageMarshalType(vmencoding.MarshalTypeConst),
			ValuesMarshalType:     storageMarshalType(vmencoding.MarshalTypeConst),
			PrecisionBits:         64,
		})...,
	)

	blockHeaders, err := unmarshalStorageBlockHeaders(data)
	if err != nil {
		t.Fatalf("unexpected error unmarshaling block headers: %s", err)
	}
	if len(blockHeaders) != 2 {
		t.Fatalf("unexpected block header count: got %d; want 2", len(blockHeaders))
	}
}