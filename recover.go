package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	vmencoding "github.com/VictoriaMetrics/VictoriaMetrics/lib/encoding"
	kzstd "github.com/klauspost/compress/zstd"
)

const (
	metaindexFilename = "metaindex.bin"
	indexFilename     = "index.bin"
	itemsFilename     = "items.bin"
	lensFilename      = "lens.bin"
	metadataFilename  = "metadata.json"
	partsFilename     = "parts.json"
	defaultFileReadChunkSize = 1 << 20
)

var fileReadChunkSize = defaultFileReadChunkSize

type recoverySummary struct {
	metaindexFiles int
	metadataFiles  int
	partsFiles     int
}

type verificationSummary struct {
	metaindexFiles int
	metadataFiles  int
	partsFiles     int
	mismatches     int
}

type metaindexRow struct {
	firstItem        []byte
	blockHeadersCount uint32
	indexBlockOffset uint64
	indexBlockSize   uint32
}

type blockHeader struct {
	commonPrefix    []byte
	firstItem       []byte
	marshalType     marshalType
	itemsCount      uint32
	itemsBlockOffset uint64
	lensBlockOffset uint64
	itemsBlockSize  uint32
	lensBlockSize   uint32
}

type marshalType uint8

const (
	marshalTypePlain = marshalType(0)
	marshalTypeZSTD  = marshalType(1)
)

type partHeader struct {
	ItemsCount  uint64    `json:"ItemsCount"`
	BlocksCount uint64    `json:"BlocksCount"`
	FirstItem   hexString `json:"FirstItem"`
	LastItem    hexString `json:"LastItem"`
}

type hexString []byte

func (hs hexString) MarshalJSON() ([]byte, error) {
	h := hex.EncodeToString(hs)
	b := make([]byte, 0, len(h)+2)
	b = append(b, '"')
	b = append(b, h...)
	b = append(b, '"')
	return b, nil
}

func (hs *hexString) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("invalid hex string JSON value %q", data)
	}
	b, err := hex.DecodeString(string(data[1 : len(data)-1]))
	if err != nil {
		return fmt.Errorf("cannot decode hex string %q: %w", data, err)
	}
	*hs = append((*hs)[:0], b...)
	return nil
}

type partScan struct {
	rows        []metaindexRow
	itemsCount  uint64
	blocksCount uint64
	firstBH     blockHeader
	lastBH      blockHeader
	hasBlocks   bool
}

type storagePartitionInfo struct {
	smallDir string
	bigDir   string
}

func printRecoveryAction(kind, path string, dryRun bool) {
	verb := "rebuilding"
	if dryRun {
		verb = "would rebuild"
	}
	fmt.Printf("%s %s: %s\n", verb, kind, path)
}

func printStorageScan(path string, scan *storagePartScan) {
	fmt.Printf("scanned storage part: %s (index blocks=%d, blocks=%d, rows=%d)\n", path, len(scan.rows), scan.blocksCount, scan.rowsCount)
}

func recoverTree(root string, dryRun, force bool) (recoverySummary, error) {
	var summary recoverySummary
	partsDirs := make(map[string]struct{})
	storagePartitions := make(map[string]*storagePartitionInfo)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}

		ok, err := isMergesetPartDir(path)
		if err != nil {
			return err
		}
		if !ok {
			okStorage, err := isStoragePartDir(path)
			if err != nil {
				return err
			}
			if !okStorage {
				return nil
			}
			registerStoragePartition(path, storagePartitions)

			needMetaindex := force || !pathExists(filepath.Join(path, metaindexFilename))
			needMetadata := force || !pathExists(filepath.Join(path, metadataFilename))
			if !needMetaindex && !needMetadata {
				return filepath.SkipDir
			}

			fmt.Printf("scanning storage part: %s\n", path)
			scan, err := scanStoragePart(path)
			if err != nil {
				return fmt.Errorf("cannot scan storage part %q: %w", path, err)
			}
			printStorageScan(path, scan)

			if needMetaindex {
				metaindexPath := filepath.Join(path, metaindexFilename)
				printRecoveryAction("storage metaindex.bin", metaindexPath, dryRun)
				if !dryRun {
					if err := writeStorageMetaindex(path, scan.rows); err != nil {
						return fmt.Errorf("cannot rebuild %q: %w", metaindexPath, err)
					}
				}
				summary.metaindexFiles++
			}

			if needMetadata {
				metadataPath := filepath.Join(path, metadataFilename)
				printRecoveryAction("storage metadata.json", metadataPath, dryRun)
				if !dryRun {
					if err := writeStorageMetadata(path, scan); err != nil {
						return fmt.Errorf("cannot rebuild %q: %w", metadataPath, err)
					}
				}
				summary.metadataFiles++
			}

			return filepath.SkipDir
		}
		partsDirs[filepath.Dir(path)] = struct{}{}

		needMetaindex := force || !pathExists(filepath.Join(path, metaindexFilename))
		needMetadata := force || !pathExists(filepath.Join(path, metadataFilename))
		if !needMetaindex && !needMetadata {
			return filepath.SkipDir
		}

		scan, err := scanPart(path)
		if err != nil {
			return fmt.Errorf("cannot scan %q: %w", path, err)
		}

		if needMetaindex {
			metaindexPath := filepath.Join(path, metaindexFilename)
			printRecoveryAction("metaindex.bin", metaindexPath, dryRun)
			if !dryRun {
				if err := writeMetaindex(path, scan.rows); err != nil {
					return fmt.Errorf("cannot rebuild %q: %w", metaindexPath, err)
				}
			}
			summary.metaindexFiles++
		}

		if needMetadata {
			metadataPath := filepath.Join(path, metadataFilename)
			printRecoveryAction("metadata.json", metadataPath, dryRun)
			if !dryRun {
				if err := writeMetadata(path, scan); err != nil {
					return fmt.Errorf("cannot rebuild %q: %w", metadataPath, err)
				}
			}
			summary.metadataFiles++
		}

		return filepath.SkipDir
	})
	if err != nil {
		return summary, err
	}

	dirs := make([]string, 0, len(partsDirs))
	for dir := range partsDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		partsPath := filepath.Join(dir, partsFilename)
		printRecoveryAction("parts.json", partsPath, dryRun)
		if !dryRun {
			if err := writePartsFile(dir); err != nil {
				return summary, fmt.Errorf("cannot rebuild %q: %w", partsPath, err)
			}
		}
		summary.partsFiles++
	}

	storagePartitionDirs := make([]string, 0, len(storagePartitions))
	for smallDir := range storagePartitions {
		storagePartitionDirs = append(storagePartitionDirs, smallDir)
	}
	sort.Strings(storagePartitionDirs)
	for _, smallDir := range storagePartitionDirs {
		info := storagePartitions[smallDir]
		partsPath := filepath.Join(info.smallDir, partsFilename)
		printRecoveryAction("storage parts.json", partsPath, dryRun)
		if !dryRun {
			if err := writeStoragePartsFile(info.smallDir, info.bigDir); err != nil {
				return summary, fmt.Errorf("cannot rebuild %q: %w", partsPath, err)
			}
		}
		summary.partsFiles++
	}

	return summary, nil
}

func verifyTree(root string) (verificationSummary, error) {
	var summary verificationSummary
	partsDirs := make(map[string]struct{})
	storagePartitions := make(map[string]*storagePartitionInfo)

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}

		ok, err := isMergesetPartDir(path)
		if err != nil {
			return err
		}
		if !ok {
			okStorage, err := isStoragePartDir(path)
			if err != nil {
				return err
			}
			if !okStorage {
				return nil
			}
			registerStoragePartition(path, storagePartitions)

			scan, err := scanStoragePart(path)
			if err != nil {
				return fmt.Errorf("cannot scan storage part %q: %w", path, err)
			}

			metaindexPath := filepath.Join(path, metaindexFilename)
			fmt.Printf("checking: %s\n", metaindexPath)
			summary.metaindexFiles++
			ok, err = verifyStorageMetaindex(metaindexPath, scan.rows)
			if err != nil {
				return err
			}
			if !ok {
				summary.mismatches++
				fmt.Printf("mismatch: %s\n", metaindexPath)
			} else {
				fmt.Printf("ok: %s\n", metaindexPath)
			}

			metadataPath := filepath.Join(path, metadataFilename)
			fmt.Printf("checking: %s\n", metadataPath)
			summary.metadataFiles++
			ok, err = verifyStorageMetadata(metadataPath, path, scan)
			if err != nil {
				return err
			}
			if !ok {
				summary.mismatches++
				fmt.Printf("mismatch: %s\n", metadataPath)
			} else {
				fmt.Printf("ok: %s\n", metadataPath)
			}

			return filepath.SkipDir
		}
		partsDirs[filepath.Dir(path)] = struct{}{}

		scan, err := scanPart(path)
		if err != nil {
			return fmt.Errorf("cannot scan %q: %w", path, err)
		}

		metaindexPath := filepath.Join(path, metaindexFilename)
		fmt.Printf("checking: %s\n", metaindexPath)
		summary.metaindexFiles++
		ok, err = verifyMetaindex(metaindexPath, scan.rows)
		if err != nil {
			return err
		}
		if !ok {
			summary.mismatches++
			fmt.Printf("mismatch: %s\n", metaindexPath)
		} else {
			fmt.Printf("ok: %s\n", metaindexPath)
		}

		metadataPath := filepath.Join(path, metadataFilename)
		fmt.Printf("checking: %s\n", metadataPath)
		summary.metadataFiles++
		ok, err = verifyMetadata(metadataPath, path, scan)
		if err != nil {
			return err
		}
		if !ok {
			summary.mismatches++
			fmt.Printf("mismatch: %s\n", metadataPath)
		} else {
			fmt.Printf("ok: %s\n", metadataPath)
		}

		return filepath.SkipDir
	})
	if err != nil {
		return summary, err
	}

	dirs := make([]string, 0, len(partsDirs))
	for dir := range partsDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		partsPath := filepath.Join(dir, partsFilename)
		fmt.Printf("checking: %s\n", partsPath)
		summary.partsFiles++
		ok, err := verifyPartsFile(partsPath, dir)
		if err != nil {
			return summary, err
		}
		if !ok {
			summary.mismatches++
			fmt.Printf("mismatch: %s\n", partsPath)
		} else {
			fmt.Printf("ok: %s\n", partsPath)
		}
	}

	storagePartitionDirs := make([]string, 0, len(storagePartitions))
	for smallDir := range storagePartitions {
		storagePartitionDirs = append(storagePartitionDirs, smallDir)
	}
	sort.Strings(storagePartitionDirs)
	for _, smallDir := range storagePartitionDirs {
		info := storagePartitions[smallDir]
		partsPath := filepath.Join(info.smallDir, partsFilename)
		fmt.Printf("checking: %s\n", partsPath)
		summary.partsFiles++
		ok, err := verifyStoragePartsFile(partsPath, info.smallDir, info.bigDir)
		if err != nil {
			return summary, err
		}
		if !ok {
			summary.mismatches++
			fmt.Printf("mismatch: %s\n", partsPath)
		} else {
			fmt.Printf("ok: %s\n", partsPath)
		}
	}

	return summary, nil
}

func verifyMetaindex(path string, want []metaindexRow) (bool, error) {
	if !pathExists(path) {
		return false, nil
	}
	got, err := readMetaindex(path)
	if err != nil {
		return false, fmt.Errorf("cannot read %q: %w", path, err)
	}
	if len(got) != len(want) {
		return false, nil
	}
	for i := range want {
		if !metaindexRowsEqual(got[i], want[i]) {
			return false, nil
		}
	}
	return true, nil
}

func verifyMetadata(metadataPath, partPath string, scan *partScan) (bool, error) {
	if !pathExists(metadataPath) {
		return false, nil
	}
	wantData, err := buildMetadataData(partPath, scan)
	if err != nil {
		return false, err
	}
	var want partHeader
	if err := json.Unmarshal(wantData, &want); err != nil {
		return false, fmt.Errorf("cannot parse expected metadata for %q: %w", metadataPath, err)
	}
	gotData, err := os.ReadFile(metadataPath)
	if err != nil {
		return false, fmt.Errorf("cannot read %q: %w", metadataPath, err)
	}
	var got partHeader
	if err := json.Unmarshal(gotData, &got); err != nil {
		return false, fmt.Errorf("cannot parse %q: %w", metadataPath, err)
	}
	return reflect.DeepEqual(got, want), nil
}

func verifyPartsFile(partsPath, dir string) (bool, error) {
	if !pathExists(partsPath) {
		return false, nil
	}
	wantData, err := buildPartsFileData(dir)
	if err != nil {
		return false, err
	}
	var want []string
	if err := json.Unmarshal(wantData, &want); err != nil {
		return false, fmt.Errorf("cannot parse expected parts file for %q: %w", partsPath, err)
	}
	gotData, err := os.ReadFile(partsPath)
	if err != nil {
		return false, fmt.Errorf("cannot read %q: %w", partsPath, err)
	}
	var got []string
	if err := json.Unmarshal(gotData, &got); err != nil {
		return false, fmt.Errorf("cannot parse %q: %w", partsPath, err)
	}
	return reflect.DeepEqual(got, want), nil
}

func isMergesetPartDir(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	for _, name := range []string{indexFilename, itemsFilename, lensFilename} {
		filePath := filepath.Join(path, name)
		info, err := os.Stat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("cannot stat %q: %w", filePath, err)
		}
		if info.IsDir() {
			return false, fmt.Errorf("expected %q to be a file", filePath)
		}
	}
	return true, nil
}

func scanPart(partPath string) (*partScan, error) {
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
	var decodedBuf []byte

	var scan partScan
	for offset := int64(0); offset < fileSize; {
		frameSize, err := readZSTDFrameSize(chunkReader, offset, fileSize)
		if err != nil {
			return nil, fmt.Errorf("cannot determine zstd frame size at offset %d in %q: %w", offset, indexPath, err)
		}
		frameData, err := chunkReader.ReadRange(offset, frameSize)
		if err != nil {
			return nil, fmt.Errorf("cannot read %d bytes at offset %d from %q: %w", frameSize, offset, indexPath, err)
		}

		decoded, err := vmencoding.DecompressZSTD(decodedBuf[:0], frameData)
		if err != nil {
			return nil, fmt.Errorf("cannot decompress index block at offset %d in %q: %w", offset, indexPath, err)
		}
		decodedBuf = decoded
		bhs, err := unmarshalBlockHeaders(decoded)
		if err != nil {
			return nil, fmt.Errorf("cannot decode index block at offset %d in %q: %w", offset, indexPath, err)
		}
		if len(bhs) == 0 {
			return nil, fmt.Errorf("decoded empty index block at offset %d in %q", offset, indexPath)
		}

		if !scan.hasBlocks {
			cloneBlockHeader(&scan.firstBH, &bhs[0])
			scan.hasBlocks = true
		}
		cloneBlockHeader(&scan.lastBH, &bhs[len(bhs)-1])

		scan.rows = append(scan.rows, metaindexRow{
			firstItem:        append([]byte{}, bhs[0].firstItem...),
			blockHeadersCount: uint32(len(bhs)),
			indexBlockOffset: uint64(offset),
			indexBlockSize:   uint32(frameSize),
		})
		scan.blocksCount += uint64(len(bhs))
		for _, bh := range bhs {
			scan.itemsCount += uint64(bh.itemsCount)
		}
		offset += int64(frameSize)
	}

	if !scan.hasBlocks {
		return nil, errors.New("no blocks found")
	}
	return &scan, nil
}

type chunkedFileReader struct {
	f         *os.File
	chunkSize int
	start     int64
	end       int64
	buf       []byte
}

func newChunkedFileReader(f *os.File, chunkSize int) *chunkedFileReader {
	if chunkSize <= 0 {
		chunkSize = defaultFileReadChunkSize
	}
	return &chunkedFileReader{
		f:         f,
		chunkSize: chunkSize,
	}
}

func (r *chunkedFileReader) ReadRange(offset int64, size int) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("invalid read size %d", size)
	}
	if size == 0 {
		return nil, nil
	}
	if offset >= r.start && offset+int64(size) <= r.end {
		return r.buf[offset-r.start : offset-r.start+int64(size)], nil
	}

	bufSize := r.chunkSize
	if size > bufSize {
		bufSize = size
	}
	if cap(r.buf) < bufSize {
		r.buf = make([]byte, bufSize)
	} else {
		r.buf = r.buf[:bufSize]
	}

	n, err := r.f.ReadAt(r.buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n < size {
		return nil, io.ErrUnexpectedEOF
	}
	r.start = offset
	r.end = offset + int64(n)
	r.buf = r.buf[:n]
	return r.buf[:size], nil
}

func writeMetaindex(partPath string, rows []metaindexRow) error {
	compressed := buildMetaindexData(rows)
	return writeFileAtomically(filepath.Join(partPath, metaindexFilename), compressed)
}

func buildMetaindexData(rows []metaindexRow) []byte {
	var raw []byte
	for i := range rows {
		raw = rows[i].Marshal(raw)
	}
	return vmencoding.CompressZSTDLevel(nil, raw, 0)
}

func writeMetadata(partPath string, scan *partScan) error {
	data, err := buildMetadataData(partPath, scan)
	if err != nil {
		return err
	}
	return writeFileAtomically(filepath.Join(partPath, metadataFilename), data)
}

func buildMetadataData(partPath string, scan *partScan) ([]byte, error) {
	itemsPath := filepath.Join(partPath, itemsFilename)
	lensPath := filepath.Join(partPath, lensFilename)

	firstItem, err := readBoundaryItem(itemsPath, lensPath, &scan.firstBH, true)
	if err != nil {
		return nil, fmt.Errorf("cannot read first item: %w", err)
	}
	lastItem, err := readBoundaryItem(itemsPath, lensPath, &scan.lastBH, false)
	if err != nil {
		return nil, fmt.Errorf("cannot read last item: %w", err)
	}

	ph := partHeader{
		ItemsCount:  scan.itemsCount,
		BlocksCount: scan.blocksCount,
		FirstItem:   hexString(firstItem),
		LastItem:    hexString(lastItem),
	}
	data, err := json.Marshal(&ph)
	if err != nil {
		return nil, fmt.Errorf("cannot marshal metadata: %w", err)
	}
	return data, nil
}

func writePartsFile(dir string) error {
	data, err := buildPartsFileData(dir)
	if err != nil {
		return err
	}
	return writeFileAtomically(filepath.Join(dir, partsFilename), data)
}

func buildPartsFileData(dir string) ([]byte, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot read %q: %w", dir, err)
	}
	partNames := make([]string, 0, len(des))
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		name := de.Name()
		if isSpecialDir(name) {
			continue
		}
		ok, err := isMergesetPartDir(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if ok {
			partNames = append(partNames, name)
		}
	}
	sort.Strings(partNames)
	data, err := json.Marshal(partNames)
	if err != nil {
		return nil, fmt.Errorf("cannot marshal part names: %w", err)
	}
	return data, nil
}

func readMetaindex(path string) ([]metaindexRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := vmencoding.DecompressZSTD(nil, data)
	if err != nil {
		return nil, err
	}
	var rows []metaindexRow
	for len(decoded) > 0 {
		var row metaindexRow
		fi, nSize := vmencoding.UnmarshalBytes(decoded)
		if nSize <= 0 {
			return nil, errors.New("cannot unmarshal metaindex firstItem")
		}
		decoded = decoded[nSize:]
		row.firstItem = append(row.firstItem[:0], fi...)
		if len(decoded) < 16 {
			return nil, errors.New("truncated metaindex row")
		}
		row.blockHeadersCount = vmencoding.UnmarshalUint32(decoded)
		decoded = decoded[4:]
		row.indexBlockOffset = vmencoding.UnmarshalUint64(decoded)
		decoded = decoded[8:]
		row.indexBlockSize = vmencoding.UnmarshalUint32(decoded)
		decoded = decoded[4:]
		rows = append(rows, row)
	}
	return rows, nil
}

func metaindexRowsEqual(a, b metaindexRow) bool {
	return a.blockHeadersCount == b.blockHeadersCount &&
		a.indexBlockOffset == b.indexBlockOffset &&
		a.indexBlockSize == b.indexBlockSize &&
		string(a.firstItem) == string(b.firstItem)
}

func readBoundaryItem(itemsPath, lensPath string, bh *blockHeader, first bool) ([]byte, error) {
	itemsData, err := readFileRange(itemsPath, int64(bh.itemsBlockOffset), int(bh.itemsBlockSize))
	if err != nil {
		return nil, err
	}
	lensData, err := readFileRange(lensPath, int64(bh.lensBlockOffset), int(bh.lensBlockSize))
	if err != nil {
		return nil, err
	}
	if first {
		return append([]byte{}, bh.firstItem...), nil
	}
	return decodeLastItem(itemsData, lensData, bh.firstItem, bh.commonPrefix, bh.itemsCount, bh.marshalType)
}

func decodeLastItem(itemsData, lensData, firstItem, commonPrefix []byte, itemsCount uint32, mt marshalType) ([]byte, error) {
	if itemsCount == 0 {
		return nil, errors.New("itemsCount must be positive")
	}
	if itemsCount == 1 {
		return append([]byte{}, firstItem...), nil
	}

	switch mt {
	case marshalTypePlain:
		return decodeLastItemPlain(itemsData, lensData, firstItem, commonPrefix, itemsCount)
	case marshalTypeZSTD:
		return decodeLastItemZSTD(itemsData, lensData, firstItem, commonPrefix, itemsCount)
	default:
		return nil, fmt.Errorf("unknown marshalType=%d", mt)
	}
}

func decodeLastItemPlain(itemsData, lensData, firstItem, commonPrefix []byte, itemsCount uint32) ([]byte, error) {
	if len(lensData) != int((itemsCount-1)*8) {
		return nil, fmt.Errorf("unexpected lensData size %d for %d items", len(lensData), itemsCount)
	}
	b := itemsData
	var lastItem []byte
	for i := 1; i < int(itemsCount); i++ {
		itemLen := vmencoding.UnmarshalUint64(lensData[(i-1)*8:])
		if uint64(len(b)) < itemLen {
			return nil, fmt.Errorf("not enough itemsData for item %d; want %d bytes; have %d", i, itemLen, len(b))
		}
		lastItem = append(lastItem[:0], commonPrefix...)
		lastItem = append(lastItem, b[:itemLen]...)
		b = b[itemLen:]
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("unexpected tail left in itemsData: %d bytes", len(b))
	}
	return append([]byte{}, lastItem...), nil
}

func decodeLastItemZSTD(itemsData, lensData, firstItem, commonPrefix []byte, itemsCount uint32) ([]byte, error) {
	decodedLens, err := vmencoding.DecompressZSTD(nil, lensData)
	if err != nil {
		return nil, fmt.Errorf("cannot decompress lensData: %w", err)
	}
	decodedItems, err := vmencoding.DecompressZSTD(nil, itemsData)
	if err != nil {
		return nil, fmt.Errorf("cannot decompress itemsData: %w", err)
	}

	xlens := make([]uint64, int(itemsCount)-1)
	tail, err := vmencoding.UnmarshalVarUint64s(xlens, decodedLens)
	if err != nil {
		return nil, fmt.Errorf("cannot decode prefixLens: %w", err)
	}
	xitemLens := make([]uint64, int(itemsCount)-1)
	tail, err = vmencoding.UnmarshalVarUint64s(xitemLens, tail)
	if err != nil {
		return nil, fmt.Errorf("cannot decode itemLens: %w", err)
	}
	if len(tail) != 0 {
		return nil, fmt.Errorf("unexpected tail left in decoded lensData: %d bytes", len(tail))
	}

	cpLen := len(commonPrefix)
	prevItem := firstItem[cpLen:]
	prevPrefixLen := uint64(0)
	prevItemLen := uint64(len(firstItem) - cpLen)
	b := decodedItems
	var lastItem []byte
	for i := 0; i < len(xlens); i++ {
		prefixLen := xlens[i] ^ prevPrefixLen
		prevPrefixLen = prefixLen
		itemLen := xitemLens[i] ^ prevItemLen
		prevItemLen = itemLen
		if prefixLen > itemLen {
			return nil, fmt.Errorf("prefixLen=%d exceeds itemLen=%d", prefixLen, itemLen)
		}
		if prefixLen > uint64(len(prevItem)) {
			return nil, fmt.Errorf("prefixLen=%d exceeds previous item len %d", prefixLen, len(prevItem))
		}
		suffixLen := itemLen - prefixLen
		if uint64(len(b)) < suffixLen {
			return nil, fmt.Errorf("not enough itemsData; need %d bytes, have %d", suffixLen, len(b))
		}
		lastItem = append(lastItem[:0], commonPrefix...)
		lastItem = append(lastItem, prevItem[:prefixLen]...)
		lastItem = append(lastItem, b[:suffixLen]...)
		b = b[suffixLen:]
		prevItem = lastItem[cpLen:]
	}
	if len(b) != 0 {
		return nil, fmt.Errorf("unexpected tail left in itemsData: %d bytes", len(b))
	}
	return append([]byte{}, lastItem...), nil
}

func readZSTDFrameSize(r *chunkedFileReader, offset, fileSize int64) (int, error) {
	headerBufSize := kzstd.HeaderMaxSize
	if remaining := int(fileSize - offset); remaining < headerBufSize {
		headerBufSize = remaining
	}
	if headerBufSize <= 0 {
		return 0, io.EOF
	}
	headerBuf, err := r.ReadRange(offset, headerBufSize)
	if err != nil {
		return 0, err
	}
	return readZSTDFrameSizeFromBytes(headerBuf, func(pos int64, size int) ([]byte, error) {
		return r.ReadRange(offset+pos, size)
	}, fileSize-offset)
}

func readZSTDFrameSizeFromData(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, io.EOF
	}
	headerBufSize := kzstd.HeaderMaxSize
	if len(data) < headerBufSize {
		headerBufSize = len(data)
	}
	return readZSTDFrameSizeFromBytes(data[:headerBufSize], func(pos int64, size int) ([]byte, error) {
		if pos < 0 || size < 0 || pos > int64(len(data))-int64(size) {
			return nil, io.ErrUnexpectedEOF
		}
		return data[pos : pos+int64(size)], nil
	}, int64(len(data)))
}

func readZSTDFrameSizeFromBytes(headerBuf []byte, readAt func(pos int64, size int) ([]byte, error), available int64) (int, error) {
	var header kzstd.Header
	if err := header.Decode(headerBuf); err != nil {
		return 0, err
	}
	if header.Skippable {
		return header.HeaderSize + int(header.SkippableSize), nil
	}

	pos := int64(header.HeaderSize)
	for {
		bh, err := readAt(pos, 3)
		if err != nil {
			return 0, err
		}
		blockHeader := uint32(bh[0]) | (uint32(bh[1]) << 8) | (uint32(bh[2]) << 16)
		last := blockHeader&1 != 0
		blockType := (blockHeader >> 1) & 3
		blockSize := int(blockHeader >> 3)
		switch blockType {
		case 0, 2:
			pos += 3 + int64(blockSize)
		case 1:
			pos += 4
		case 3:
			return 0, fmt.Errorf("encountered reserved zstd block type at relative offset %d", pos)
		default:
			return 0, fmt.Errorf("unexpected zstd block type %d", blockType)
		}
		if last {
			break
		}
	}
	if header.HasCheckSum {
		pos += 4
	}
	if pos > available {
		return 0, fmt.Errorf("frame exceeds available data: need %d bytes; have %d", pos, available)
	}
	return int(pos), nil
}

func unmarshalBlockHeaders(src []byte) ([]blockHeader, error) {
	var bhs []blockHeader
	for len(src) > 0 {
		var bh blockHeader
		tail, err := bh.Unmarshal(src)
		if err != nil {
			return nil, err
		}
		bhs = append(bhs, bh)
		src = tail
	}
	if len(bhs) == 0 {
		return nil, errors.New("no block headers decoded")
	}
	for i := 1; i < len(bhs); i++ {
		if string(bhs[i-1].firstItem) >= string(bhs[i].firstItem) {
			return nil, fmt.Errorf("block headers must be sorted by firstItem")
		}
	}
	return bhs, nil
}

func (bh *blockHeader) Unmarshal(src []byte) ([]byte, error) {
	cp, nSize := vmencoding.UnmarshalBytes(src)
	if nSize <= 0 {
		return src, errors.New("cannot unmarshal commonPrefix")
	}
	src = src[nSize:]
	bh.commonPrefix = append(bh.commonPrefix[:0], cp...)

	fi, nSize := vmencoding.UnmarshalBytes(src)
	if nSize <= 0 {
		return src, errors.New("cannot unmarshal firstItem")
	}
	src = src[nSize:]
	bh.firstItem = append(bh.firstItem[:0], fi...)

	if len(src) < 1 {
		return src, errors.New("cannot unmarshal marshalType")
	}
	bh.marshalType = marshalType(src[0])
	src = src[1:]
	if bh.marshalType != marshalTypePlain && bh.marshalType != marshalTypeZSTD {
		return src, fmt.Errorf("unexpected marshalType=%d", bh.marshalType)
	}
	if len(src) < 4 {
		return src, errors.New("cannot unmarshal itemsCount")
	}
	bh.itemsCount = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	if len(src) < 8 {
		return src, errors.New("cannot unmarshal itemsBlockOffset")
	}
	bh.itemsBlockOffset = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	if len(src) < 8 {
		return src, errors.New("cannot unmarshal lensBlockOffset")
	}
	bh.lensBlockOffset = vmencoding.UnmarshalUint64(src)
	src = src[8:]
	if len(src) < 4 {
		return src, errors.New("cannot unmarshal itemsBlockSize")
	}
	bh.itemsBlockSize = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	if len(src) < 4 {
		return src, errors.New("cannot unmarshal lensBlockSize")
	}
	bh.lensBlockSize = vmencoding.UnmarshalUint32(src)
	src = src[4:]
	if bh.itemsCount == 0 {
		return src, errors.New("itemsCount must be positive")
	}
	return src, nil
}

func (mr *metaindexRow) Marshal(dst []byte) []byte {
	dst = vmencoding.MarshalBytes(dst, mr.firstItem)
	dst = vmencoding.MarshalUint32(dst, mr.blockHeadersCount)
	dst = vmencoding.MarshalUint64(dst, mr.indexBlockOffset)
	dst = vmencoding.MarshalUint32(dst, mr.indexBlockSize)
	return dst
}

func cloneBlockHeader(dst, src *blockHeader) {
	dst.commonPrefix = append(dst.commonPrefix[:0], src.commonPrefix...)
	dst.firstItem = append(dst.firstItem[:0], src.firstItem...)
	dst.marshalType = src.marshalType
	dst.itemsCount = src.itemsCount
	dst.itemsBlockOffset = src.itemsBlockOffset
	dst.lensBlockOffset = src.lensBlockOffset
	dst.itemsBlockSize = src.itemsBlockSize
	dst.lensBlockSize = src.lensBlockSize
}

func readFileRange(path string, offset int64, size int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open %q: %w", path, err)
	}
	defer f.Close()
	return readFileRangeFromOpenFile(f, offset, size)
}

func readFileRangeFromOpenFile(f *os.File, offset int64, size int) ([]byte, error) {
	buf := make([]byte, size)
	if size == 0 {
		return buf, nil
	}
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, err
	}
	return buf, nil
}

func writeFileAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".rebuild-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isSpecialDir(name string) bool {
	return name == "tmp" || name == "txn" || name == "snapshots" || name == "cache"
}