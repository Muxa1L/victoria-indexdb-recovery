package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	vmencoding "github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
	"github.com/VictoriaMetrics/VictoriaMetrics/lib/timeutil"
)

const (
	timestampsFilename       = "timestamps.bin"
	valuesFilename           = "values.bin"
	storageSmallDirname      = "small"
	storageBigDirname        = "big"
	storageMaxRowsPerBlock   = 8 * 1024
	storageMaxBlockSize      = 8 * storageMaxRowsPerBlock
	storageMarshaledTSIDSize = 32
)

type storageTSID struct {
	AccountID     uint32
	ProjectID     uint32
	MetricGroupID uint64
	JobID         uint32
	InstanceID    uint32
	MetricID      uint64
}

func (tsid storageTSID) Marshal(dst []byte) []byte {
	dst = vmencoding.MarshalUint32(dst, tsid.AccountID)
	dst = vmencoding.MarshalUint32(dst, tsid.ProjectID)
	dst = vmencoding.MarshalUint64(dst, tsid.MetricGroupID)
	dst = vmencoding.MarshalUint32(dst, tsid.JobID)
	dst = vmencoding.MarshalUint32(dst, tsid.InstanceID)
	dst = vmencoding.MarshalUint64(dst, tsid.MetricID)
	return dst
}

func (tsid *storageTSID) Unmarshal(src []byte) ([]byte, error) {
	if len(src) < storageMarshaledTSIDSize {
		return nil, fmt.Errorf("too short TSID: got %d bytes; want %d bytes", len(src), storageMarshaledTSIDSize)
	}
	tsid.AccountID = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	tsid.ProjectID = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	tsid.MetricGroupID = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	tsid.JobID = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	tsid.InstanceID = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	tsid.MetricID = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	return src, nil
}

func (tsid *storageTSID) Less(other *storageTSID) bool {
	if tsid.AccountID != other.AccountID {
		return tsid.AccountID < other.AccountID
	}
	if tsid.ProjectID != other.ProjectID {
		return tsid.ProjectID < other.ProjectID
	}
	if tsid.MetricGroupID != other.MetricGroupID {
		return tsid.MetricGroupID < other.MetricGroupID
	}
	if tsid.JobID != other.JobID {
		return tsid.JobID < other.JobID
	}
	if tsid.InstanceID != other.InstanceID {
		return tsid.InstanceID < other.InstanceID
	}
	return tsid.MetricID < other.MetricID
}

type storageMarshalType byte

type storageBlockHeader struct {
	TSID                   storageTSID
	MinTimestamp           int64
	MaxTimestamp           int64
	FirstValue             int64
	TimestampsBlockOffset  uint64
	ValuesBlockOffset      uint64
	TimestampsBlockSize    uint32
	ValuesBlockSize        uint32
	RowsCount              uint32
	Scale                  int16
	TimestampsMarshalType  storageMarshalType
	ValuesMarshalType      storageMarshalType
	PrecisionBits          uint8
}

func (bh *storageBlockHeader) Marshal(dst []byte) []byte {
	dst = bh.TSID.Marshal(dst)
	dst = vmencoding.MarshalInt64(dst, bh.MinTimestamp)
	dst = vmencoding.MarshalInt64(dst, bh.MaxTimestamp)
	dst = vmencoding.MarshalInt64(dst, bh.FirstValue)
	dst = vmencoding.MarshalUint64(dst, bh.TimestampsBlockOffset)
	dst = vmencoding.MarshalUint64(dst, bh.ValuesBlockOffset)
	dst = vmencoding.MarshalUint32(dst, bh.TimestampsBlockSize)
	dst = vmencoding.MarshalUint32(dst, bh.ValuesBlockSize)
	dst = vmencoding.MarshalUint32(dst, bh.RowsCount)
	dst = vmencoding.MarshalInt16(dst, bh.Scale)
	dst = append(dst, byte(bh.TimestampsMarshalType), byte(bh.ValuesMarshalType), bh.PrecisionBits)
	return dst
}

var storageMarshaledBlockHeaderSize = func() int {
	var bh storageBlockHeader
	return len(bh.Marshal(nil))
}()

func (bh *storageBlockHeader) Unmarshal(src []byte) ([]byte, error) {
	if len(src) < storageMarshaledBlockHeaderSize {
		return src, fmt.Errorf("too short block header; got %d bytes; want %d bytes", len(src), storageMarshaledBlockHeaderSize)
	}

	tail, err := bh.TSID.Unmarshal(src)
	if err != nil {
		return src, fmt.Errorf("cannot unmarshal TSID: %w", err)
	}
	src = tail

	bh.MinTimestamp = vmencoding.UnmarshalInt64(src)
	src = src[8:]
	bh.MaxTimestamp = vmencoding.UnmarshalInt64(src)
	src = src[8:]
	bh.FirstValue = vmencoding.UnmarshalInt64(src)
	src = src[8:]
	bh.TimestampsBlockOffset = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	bh.ValuesBlockOffset = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	bh.TimestampsBlockSize = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	bh.ValuesBlockSize = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	bh.RowsCount = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	bh.Scale = vmencoding.UnmarshalInt16(src)
	src = src[2:]
	bh.TimestampsMarshalType = storageMarshalType(src[0])
	src = src[1:]
	bh.ValuesMarshalType = storageMarshalType(src[0])
	src = src[1:]
	bh.PrecisionBits = src[0]
	src = src[1:]

	if err := bh.validate(); err != nil {
		return src, err
	}
	return src, nil
}

func (bh *storageBlockHeader) Less(other *storageBlockHeader) bool {
	if bh.TSID.MetricID == other.TSID.MetricID {
		return bh.MinTimestamp < other.MinTimestamp
	}
	return bh.TSID.Less(&other.TSID)
}

func (bh *storageBlockHeader) validate() error {
	if bh.RowsCount == 0 {
		return fmt.Errorf("RowsCount in block header cannot be zero")
	}
	if bh.RowsCount > 2*storageMaxRowsPerBlock {
		return fmt.Errorf("too big RowsCount; got %d; cannot exceed %d", bh.RowsCount, 2*storageMaxRowsPerBlock)
	}
	if err := vmencoding.CheckMarshalType(vmencoding.MarshalType(bh.TimestampsMarshalType)); err != nil {
		return fmt.Errorf("unsupported TimestampsMarshalType: %w", err)
	}
	if err := vmencoding.CheckMarshalType(vmencoding.MarshalType(bh.ValuesMarshalType)); err != nil {
		return fmt.Errorf("unsupported ValuesMarshalType: %w", err)
	}
	if err := vmencoding.CheckPrecisionBits(bh.PrecisionBits); err != nil {
		return err
	}
	if bh.TimestampsBlockSize > 2*storageMaxBlockSize {
		return fmt.Errorf("too big TimestampsBlockSize; got %d; cannot exceed %d", bh.TimestampsBlockSize, 2*storageMaxBlockSize)
	}
	if bh.ValuesBlockSize > 2*storageMaxBlockSize {
		return fmt.Errorf("too big ValuesBlockSize; got %d; cannot exceed %d", bh.ValuesBlockSize, 2*storageMaxBlockSize)
	}
	return nil
}

type storageMetaindexRow struct {
	TSID              storageTSID
	BlockHeadersCount uint32
	MinTimestamp      int64
	MaxTimestamp      int64
	IndexBlockOffset  uint64
	IndexBlockSize    uint32
}

func (mr *storageMetaindexRow) RegisterBlockHeader(bh *storageBlockHeader) {
	mr.BlockHeadersCount++
	if mr.BlockHeadersCount == 1 {
		mr.TSID = bh.TSID
		mr.MinTimestamp = bh.MinTimestamp
		mr.MaxTimestamp = bh.MaxTimestamp
		return
	}
	if bh.MinTimestamp < mr.MinTimestamp {
		mr.MinTimestamp = bh.MinTimestamp
	}
	if bh.MaxTimestamp > mr.MaxTimestamp {
		mr.MaxTimestamp = bh.MaxTimestamp
	}
}

func (mr *storageMetaindexRow) Marshal(dst []byte) []byte {
	dst = mr.TSID.Marshal(dst)
	dst = vmencoding.MarshalUint32(dst, mr.BlockHeadersCount)
	dst = vmencoding.MarshalInt64(dst, mr.MinTimestamp)
	dst = vmencoding.MarshalInt64(dst, mr.MaxTimestamp)
	dst = vmencoding.MarshalUint64(dst, mr.IndexBlockOffset)
	dst = vmencoding.MarshalUint32(dst, mr.IndexBlockSize)
	return dst
}

func (mr *storageMetaindexRow) Unmarshal(src []byte) ([]byte, error) {
	tail, err := mr.TSID.Unmarshal(src)
	if err != nil {
		return src, fmt.Errorf("cannot unmarshal TSID: %w", err)
	}
	src = tail
	if len(src) < 4 {
		return src, fmt.Errorf("cannot unmarshal BlockHeadersCount from %d bytes; want at least %d bytes", len(src), 4)
	}
	mr.BlockHeadersCount = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	if len(src) < 8 {
		return src, fmt.Errorf("cannot unmarshal MinTimestamp from %d bytes; want at least %d bytes", len(src), 8)
	}
	mr.MinTimestamp = vmencoding.UnmarshalInt64(src)
	src = src[8:]
	if len(src) < 8 {
		return src, fmt.Errorf("cannot unmarshal MaxTimestamp from %d bytes; want at least %d bytes", len(src), 8)
	}
	mr.MaxTimestamp = vmencoding.UnmarshalInt64(src)
	src = src[8:]
	if len(src) < 8 {
		return src, fmt.Errorf("cannot unmarshal IndexBlockOffset from %d bytes; want at least %d bytes", len(src), 8)
	}
	mr.IndexBlockOffset = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	if len(src) < 4 {
		return src, fmt.Errorf("cannot unmarshal IndexBlockSize from %d bytes; want at least %d bytes", len(src), 4)
	}
	mr.IndexBlockSize = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	if mr.BlockHeadersCount == 0 {
		return src, fmt.Errorf("BlockHeadersCount must be greater than 0")
	}
	if mr.IndexBlockSize > 2*storageMaxBlockSize {
		return src, fmt.Errorf("too big IndexBlockSize; got %d; cannot exceed %d", mr.IndexBlockSize, 2*storageMaxBlockSize)
	}
	return src, nil
}

type storagePartHeader struct {
	RowsCount        uint64 `json:"RowsCount"`
	BlocksCount      uint64 `json:"BlocksCount"`
	MinTimestamp     int64  `json:"MinTimestamp"`
	MaxTimestamp     int64  `json:"MaxTimestamp"`
	MinDedupInterval int64  `json:"MinDedupInterval"`
}

type storagePartScan struct {
	rows             []storageMetaindexRow
	rowsCount        uint64
	blocksCount      uint64
	minTimestamp     int64
	maxTimestamp     int64
	minDedupInterval int64
	hasBlocks        bool
}

type storagePartNamesJSON struct {
	Small []string `json:"Small"`
	Big   []string `json:"Big"`
}

func isStoragePartDir(path string) (bool, error) {
	if filepath.Base(path) == storageSmallDirname || filepath.Base(path) == storageBigDirname {
		return false, nil
	}
	if isSpecialDir(filepath.Base(path)) {
		return false, nil
	}
	for _, name := range []string{indexFilename, timestampsFilename, valuesFilename} {
		fi, err := os.Stat(filepath.Join(path, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if fi.IsDir() {
			return false, fmt.Errorf("%q must be a file", filepath.Join(path, name))
		}
	}
	return true, nil
}

func registerStoragePartition(partPath string, partitions map[string]*storagePartitionInfo) {
	partitionDir := filepath.Dir(partPath)
	kindDir := filepath.Dir(partitionDir)
	rootDir := filepath.Dir(kindDir)
	kind := filepath.Base(kindDir)
	partitionName := filepath.Base(partitionDir)
	smallDir := filepath.Join(rootDir, storageSmallDirname, partitionName)
	bigDir := filepath.Join(rootDir, storageBigDirname, partitionName)
	if kind == storageBigDirname {
		smallDir = filepath.Join(rootDir, storageSmallDirname, partitionName)
		bigDir = partitionDir
	}
	if kind == storageSmallDirname {
		smallDir = partitionDir
	}
	partitions[smallDir] = &storagePartitionInfo{
		smallDir: smallDir,
		bigDir:   bigDir,
	}
}

func scanStoragePart(partPath string) (*storagePartScan, error) {
	indexPath := filepath.Join(partPath, indexFilename)
	f, err := os.Open(indexPath)
	if err != nil {
		return nil, fmt.Errorf("cannot open %q: %w", indexPath, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("cannot stat %q: %w", indexPath, err)
	}
	fileSize := fi.Size()
	chunkReader := newChunkedFileReader(f, fileReadChunkSize)
	minDedupInterval, err := readStorageMinDedupInterval(partPath)
	if err != nil {
		return nil, err
	}

	scan := &storagePartScan{
		minTimestamp:     math.MaxInt64,
		maxTimestamp:     math.MinInt64,
		minDedupInterval: minDedupInterval,
	}
	var decodedBuf []byte

	for offset := int64(0); offset < fileSize; {
		frameSize, err := readZSTDFrameSize(chunkReader, offset, fileSize)
		if err != nil {
			return nil, fmt.Errorf("cannot determine zstd frame size in %q at offset %d: %w", indexPath, offset, err)
		}
		if frameSize <= 0 || int64(frameSize) > fileSize-offset {
			return nil, fmt.Errorf("invalid zstd frame size %d in %q at offset %d", frameSize, indexPath, offset)
		}

		frame, err := chunkReader.ReadRange(offset, frameSize)
		if err != nil {
			return nil, fmt.Errorf("cannot read %d bytes at offset %d from %q: %w", frameSize, offset, indexPath, err)
		}
		decoded, err := vmencoding.DecompressZSTD(decodedBuf[:0], frame)
		if err != nil {
			return nil, fmt.Errorf("cannot decompress index block at offset %d in %q: %w", offset, indexPath, err)
		}
		decodedBuf = decoded
		blockHeaders, err := unmarshalStorageBlockHeaders(decoded)
		if err != nil {
			return nil, fmt.Errorf("cannot decode index block at offset %d in %q: %w", offset, indexPath, err)
		}

		var row storageMetaindexRow
		row.IndexBlockOffset = uint64(offset)
		row.IndexBlockSize = uint32(frameSize)
		for i := range blockHeaders {
			bh := &blockHeaders[i]
			row.RegisterBlockHeader(bh)
			scan.rowsCount += uint64(bh.RowsCount)
			scan.blocksCount++
			if bh.MinTimestamp < scan.minTimestamp {
				scan.minTimestamp = bh.MinTimestamp
			}
			if bh.MaxTimestamp > scan.maxTimestamp {
				scan.maxTimestamp = bh.MaxTimestamp
			}
			scan.hasBlocks = true
		}
		if row.BlockHeadersCount == 0 {
			return nil, fmt.Errorf("decoded empty storage index block at offset %d in %q", offset, indexPath)
		}
		scan.rows = append(scan.rows, row)

		offset += int64(frameSize)
	}

	if !scan.hasBlocks {
		return nil, fmt.Errorf("part %q does not contain any block headers", partPath)
	}
	return scan, nil
}

func unmarshalStorageBlockHeaders(src []byte) ([]storageBlockHeader, error) {
	var dst []storageBlockHeader
	for len(src) > 0 {
		var bh storageBlockHeader
		tail, err := bh.Unmarshal(src)
		if err != nil {
			return nil, fmt.Errorf("cannot unmarshal block header: %w", err)
		}
		dst = append(dst, bh)
		src = tail
	}
	if len(dst) == 0 {
		return nil, fmt.Errorf("expecting non-zero block headers; got zero")
	}
	if !sort.SliceIsSorted(dst, func(i, j int) bool { return dst[i].Less(&dst[j]) }) {
		return nil, fmt.Errorf("block headers must be sorted by tsid")
	}
	return dst, nil
}

func readStorageMinDedupInterval(partPath string) (int64, error) {
	filePath := filepath.Join(partPath, "min_dedup_interval")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("cannot read %q: %w", filePath, err)
	}
	dedupInterval, err := timeutil.ParseDuration(string(data))
	if err != nil {
		return 0, fmt.Errorf("cannot parse minimum dedup interval %q at %q: %w", data, filePath, err)
	}
	return dedupInterval.Milliseconds(), nil
}

func writeStorageMetaindex(partPath string, rows []storageMetaindexRow) error {
	metaindexPath := filepath.Join(partPath, metaindexFilename)
	return writeFileAtomically(metaindexPath, buildStorageMetaindexData(rows))
}

func buildStorageMetaindexData(rows []storageMetaindexRow) []byte {
	var raw []byte
	for i := range rows {
		raw = rows[i].Marshal(raw)
	}
	return vmencoding.CompressZSTDLevel(nil, raw, 0)
}

func writeStorageMetadata(partPath string, scan *storagePartScan) error {
	metadataPath := filepath.Join(partPath, metadataFilename)
	data, err := buildStorageMetadataData(scan)
	if err != nil {
		return err
	}
	return writeFileAtomically(metadataPath, data)
}

func buildStorageMetadataData(scan *storagePartScan) ([]byte, error) {
	ph := storagePartHeader{
		RowsCount:        scan.rowsCount,
		BlocksCount:      scan.blocksCount,
		MinTimestamp:     scan.minTimestamp,
		MaxTimestamp:     scan.maxTimestamp,
		MinDedupInterval: scan.minDedupInterval,
	}
	return json.Marshal(&ph)
}

func writeStoragePartsFile(smallDir, bigDir string) error {
	partsPath := filepath.Join(smallDir, partsFilename)
	data, err := buildStoragePartsFileData(smallDir, bigDir)
	if err != nil {
		return err
	}
	return writeFileAtomically(partsPath, data)
}

func buildStoragePartsFileData(smallDir, bigDir string) ([]byte, error) {
	smallNames, err := readStoragePartNames(smallDir)
	if err != nil {
		return nil, err
	}
	bigNames, err := readStoragePartNames(bigDir)
	if err != nil {
		return nil, err
	}
	return json.Marshal(&storagePartNamesJSON{
		Small: smallNames,
		Big:   bigNames,
	})
}

func readStoragePartNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read %q: %w", dir, err)
	}
	partNames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if isSpecialDir(entry.Name()) {
			continue
		}
		partPath := filepath.Join(dir, entry.Name())
		ok, err := isStoragePartDir(partPath)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		partNames = append(partNames, entry.Name())
	}
	sort.Strings(partNames)
	return partNames, nil
}

func verifyStorageMetaindex(path string, want []storageMetaindexRow) (bool, error) {
	if !pathExists(path) {
		return false, nil
	}
	got, err := readStorageMetaindex(path)
	if err != nil {
		return false, fmt.Errorf("cannot read %q: %w", path, err)
	}
	if len(got) != len(want) {
		return false, nil
	}
	for i := range got {
		if !storageMetaindexRowsEqual(got[i], want[i]) {
			return false, nil
		}
	}
	return true, nil
}

func readStorageMetaindex(path string) ([]storageMetaindexRow, error) {
	compressed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data, err := vmencoding.DecompressZSTD(nil, compressed)
	if err != nil {
		return nil, fmt.Errorf("cannot decompress %q: %w", path, err)
	}
	var rows []storageMetaindexRow
	for len(data) > 0 {
		var row storageMetaindexRow
		tail, err := row.Unmarshal(data)
		if err != nil {
			return nil, fmt.Errorf("cannot unmarshal storage metaindex row #%d: %w", len(rows), err)
		}
		rows = append(rows, row)
		data = tail
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("expecting non-zero metaindex rows; got zero")
	}
	if !sort.SliceIsSorted(rows, func(i, j int) bool { return rows[i].TSID.Less(&rows[j].TSID) }) {
		return nil, fmt.Errorf("metaindex rows must be sorted by TSID")
	}
	return rows, nil
}

func storageMetaindexRowsEqual(a, b storageMetaindexRow) bool {
	return a.TSID == b.TSID &&
		a.BlockHeadersCount == b.BlockHeadersCount &&
		a.MinTimestamp == b.MinTimestamp &&
		a.MaxTimestamp == b.MaxTimestamp &&
		a.IndexBlockOffset == b.IndexBlockOffset &&
		a.IndexBlockSize == b.IndexBlockSize
}

func verifyStorageMetadata(metadataPath, _ string, scan *storagePartScan) (bool, error) {
	if !pathExists(metadataPath) {
		return false, nil
	}
	want, err := buildStorageMetadataData(scan)
	if err != nil {
		return false, err
	}
	got, err := os.ReadFile(metadataPath)
	if err != nil {
		return false, fmt.Errorf("cannot read %q: %w", metadataPath, err)
	}
	var gotHeader storagePartHeader
	if err := json.Unmarshal(got, &gotHeader); err != nil {
		return false, fmt.Errorf("cannot parse %q: %w", metadataPath, err)
	}
	var wantHeader storagePartHeader
	if err := json.Unmarshal(want, &wantHeader); err != nil {
		return false, fmt.Errorf("cannot parse expected storage metadata for %q: %w", metadataPath, err)
	}
	return reflect.DeepEqual(gotHeader, wantHeader), nil
}

func verifyStoragePartsFile(partsPath, smallDir, bigDir string) (bool, error) {
	if !pathExists(partsPath) {
		return false, nil
	}
	want, err := buildStoragePartsFileData(smallDir, bigDir)
	if err != nil {
		return false, err
	}
	got, err := os.ReadFile(partsPath)
	if err != nil {
		return false, fmt.Errorf("cannot read %q: %w", partsPath, err)
	}
	var gotNames storagePartNamesJSON
	if err := json.Unmarshal(got, &gotNames); err != nil {
		return false, fmt.Errorf("cannot parse %q: %w", partsPath, err)
	}
	var wantNames storagePartNamesJSON
	if err := json.Unmarshal(want, &wantNames); err != nil {
		return false, fmt.Errorf("cannot parse expected storage parts data for %q: %w", partsPath, err)
	}
	return reflect.DeepEqual(gotNames, wantNames), nil
}