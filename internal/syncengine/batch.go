package syncengine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"folderbridge/internal/fileops"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Small files are streamed as a single gzip-compressed TAR archive.
// Each file is still checked independently, without buffering the archive.
const (
	maxBatchEntries         = 128
	maxBatchBytes     int64 = 8 << 20
	maxBatchFileBytes int64 = 128 << 10
	minBatchEntries         = 8
)

type batchFile struct {
	Path                string   `json:"path"`
	Info                fileInfo `json:"info"`
	ExpectedDestination string   `json:"expectedDestination"`
}

func validDigest(s string, allowEmpty bool) bool {
	if s == "" {
		return allowEmpty
	}
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func validateBatchPlan(root string, files []batchFile) error {
	if len(files) == 0 || len(files) > maxBatchEntries {
		return errors.New("invalid batch item count")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range files {
		if _, err := fileops.CheckedPath(root, f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("duplicate batch path %s", f.Path)
		}
		seen[f.Path] = true
		if !validDigest(f.Info.Hash, false) || !validDigest(f.ExpectedDestination, true) {
			return errors.New("invalid batch hash")
		}
		if f.Info.Size < 0 || f.Info.Size > maxBatchFileBytes {
			return errors.New("batch file too large")
		}
		total += f.Info.Size
		if total > maxBatchBytes {
			return errors.New("batch exceeds size limit")
		}
	}
	return nil
}

func (h *host) batchGet(w http.ResponseWriter, r *http.Request) {
	h.fileMu.RLock()
	defer h.fileMu.RUnlock()
	var requested []batchFile
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10)).Decode(&requested); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateBatchPlan(h.root, requested); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	checker := newPathIgnoreChecker(h.root)
	// Validate all members before writing response headers. A second verification
	// at the receiving end catches changes while the archive is being streamed.
	for _, f := range requested {
		ignored, err := checker.isIgnored(f.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if ignored {
			http.Error(w, "path is ignored", http.StatusConflict)
			return
		}
		filename, _ := fileops.CheckedPath(h.root, f.Path)
		hash, err := fileops.CurrentHash(filename)
		if err != nil || hash != f.Info.Hash {
			http.Error(w, "file changed during sync", http.StatusConflict)
			return
		}
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("X-FolderBridge-Batch", "tar.gz-v1")
	gzipWriter, _ := gzip.NewWriterLevel(w, compressionForBatch(requested))
	tarWriter := tar.NewWriter(gzipWriter)
	sent := 0
	for _, f := range requested {
		if r.Context().Err() != nil {
			break
		}
		filename, _ := fileops.CheckedPath(h.root, f.Path)
		file, err := os.Open(filename)
		if err != nil {
			break
		}
		info, err := file.Stat()
		if err != nil || info.Size() != f.Info.Size {
			file.Close()
			break
		}
		hdr := &tar.Header{Name: f.Path, Mode: 0600, Size: f.Info.Size, ModTime: time.Unix(0, f.Info.MTime), Typeflag: tar.TypeReg}
		if err = tarWriter.WriteHeader(hdr); err == nil {
			var n int64
			n, err = io.CopyN(tarWriter, file, f.Info.Size)
			if n != f.Info.Size && err == nil {
				err = io.ErrUnexpectedEOF
			}
		}
		file.Close()
		if err != nil {
			break
		}
		sent++
	}
	tarWriter.Close()
	gzipWriter.Close()
	if sent == len(requested) && h.onEvent != nil {
		h.onEvent(fmt.Sprintf("Sent batch of %d small files", sent))
	}
	// On a mid-stream read error the receiver will reject the missing members.
}

func (h *host) batchPut(w http.ResponseWriter, r *http.Request) {
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	if r.Header.Get("Content-Type") != "application/gzip" {
		http.Error(w, "expected application/gzip", 415)
		return
	}
	gzipReader, err := gzip.NewReader(http.MaxBytesReader(w, r.Body, 32<<20))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	defer gzipReader.Close()
	// Bound decompressed TAR data too, not just compressed request bytes.
	archive := tar.NewReader(io.LimitReader(gzipReader, maxBatchBytes+(2<<20)))
	count := 0
	defer func() {
		if count > 0 {
			h.signalChange()
		}
	}()
	var total int64
	seen := map[string]bool{}
	checker := newPathIgnoreChecker(h.root)
	for {
		hdr, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			http.Error(w, "nonregular batch entry", 400)
			return
		}
		if count >= maxBatchEntries || hdr.Size < 0 || hdr.Size > maxBatchFileBytes {
			http.Error(w, "invalid batch size", 400)
			return
		}
		total += hdr.Size
		if total > maxBatchBytes || seen[hdr.Name] {
			http.Error(w, "duplicate or oversized batch", 400)
			return
		}
		seen[hdr.Name] = true
		filename, err := fileops.CheckedPath(h.root, hdr.Name)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ignored, err := checker.isIgnored(hdr.Name)
		if err != nil || ignored {
			http.Error(w, "batch path ignored or invalid", 409)
			return
		}
		hash := hdr.PAXRecords["FB.hash"]
		expected := hdr.PAXRecords["FB.expected"]
		nanos, err := strconv.ParseInt(hdr.PAXRecords["FB.mtime"], 10, 64)
		if err != nil || !validDigest(hash, false) || !validDigest(expected, true) {
			http.Error(w, "invalid batch metadata", 400)
			return
		}
		if err = fileops.CheckExpected(filename, expected); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		if err = fileops.StagedWriteGuarded(filename, archive, hash, time.Unix(0, nanos), &expected); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		h.scanner.Invalidate(hdr.Name, false)
		// An incoming ignore file can change the policy for subsequent entries.
		if isIgnoreFilename(filepath.Base(hdr.Name)) {
			checker = newPathIgnoreChecker(h.root)
		}
		count++
	}
	// Read through gzip trailer, including CRC, before declaring success.
	if _, err = io.Copy(io.Discard, gzipReader); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if count == 0 {
		http.Error(w, "empty batch", 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if h.onEvent != nil {
		h.onEvent(fmt.Sprintf("Received batch of %d small files", count))
	}
}

func (p *peer) getBatch(ctx context.Context, root string, files []batchFile) ([]string, error) {
	if err := validateBatchPlan(root, files); err != nil {
		return nil, err
	}
	body, err := json.Marshal(files)
	if err != nil {
		return nil, err
	}
	endpoint := p.batchGetEndpoint
	if endpoint == "" {
		endpoint = "/v2/batch/get"
	}
	resp, err := p.request(ctx, "POST", endpoint, strings.NewReader(string(body)), map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Header.Get("X-FolderBridge-Batch") != "tar.gz-v1" {
		return nil, errors.New("unexpected batch response")
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	archive := tar.NewReader(gz)
	successes := make([]string, 0, len(files))
	for _, item := range files {
		hdr, err := archive.Next()
		if err != nil {
			return successes, fmt.Errorf("missing batch member %s: %w", item.Path, err)
		}
		if hdr.Name != item.Path || hdr.Size != item.Info.Size || (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) {
			return successes, errors.New("unexpected TAR file or size")
		}
		if ignored, err := localIgnoreForPath(root, item.Path); err != nil {
			return successes, err
		} else if ignored {
			return successes, errors.New("destination became ignored during batch")
		}
		dest, err := fileops.CheckedPath(root, item.Path)
		if err != nil {
			return successes, err
		}
		if err = fileops.StagedWriteGuarded(dest, archive, item.Info.Hash, time.Unix(0, item.Info.MTime), &item.ExpectedDestination); err != nil {
			return successes, err
		}
		successes = append(successes, item.Path)
	}
	_, err = archive.Next()
	if !errors.Is(err, io.EOF) {
		if err == nil {
			return successes, errors.New("unexpected extra batch member")
		}
		return successes, err
	}
	if _, err = io.Copy(io.Discard, gz); err != nil {
		return successes, err
	}
	return successes, nil
}

func (p *peer) putBatch(ctx context.Context, root string, files []batchFile) error {
	if err := validateBatchPlan(root, files); err != nil {
		return err
	}
	reader, writer := io.Pipe()
	sent := make(chan error, 1)
	go func() {
		gz, _ := gzip.NewWriterLevel(writer, compressionForBatch(files))
		archive := tar.NewWriter(gz)
		var writeErr error
		for _, item := range files {
			if ctx.Err() != nil {
				writeErr = ctx.Err()
				break
			}
			filename, _ := fileops.CheckedPath(root, item.Path)
			file, err := os.Open(filename)
			if err != nil {
				writeErr = err
				break
			}
			info, err := file.Stat()
			if err != nil || info.Size() != item.Info.Size {
				file.Close()
				writeErr = errors.New("file changed before batch upload")
				break
			}
			hdr := &tar.Header{Name: item.Path, Mode: 0600, Size: item.Info.Size, ModTime: time.Unix(0, item.Info.MTime), Typeflag: tar.TypeReg,
				PAXRecords: map[string]string{"FB.hash": item.Info.Hash, "FB.expected": item.ExpectedDestination, "FB.mtime": strconv.FormatInt(item.Info.MTime, 10)}}
			if err = archive.WriteHeader(hdr); err == nil {
				hasher := sha256.New()
				_, err = io.CopyN(archive, io.TeeReader(file, hasher), item.Info.Size)
				if err == nil && hex.EncodeToString(hasher.Sum(nil)) != item.Info.Hash {
					err = errors.New("file changed during batch upload")
				}
			}
			file.Close()
			if err != nil {
				writeErr = err
				break
			}
		}
		if err := archive.Close(); writeErr == nil {
			writeErr = err
		}
		if err := gz.Close(); writeErr == nil {
			writeErr = err
		}
		writer.CloseWithError(writeErr)
		sent <- writeErr
	}()
	resp, requestErr := p.request(ctx, "POST", "/v2/batch/put", reader, map[string]string{"Content-Type": "application/gzip"})
	if requestErr != nil {
		reader.CloseWithError(requestErr)
	} else {
		resp.Body.Close()
	}
	// Close always unlocks the producer when the HTTP request fails early.
	transferErr := <-sent
	if requestErr != nil {
		return requestErr
	}
	return transferErr
}

// selectBatchChunks groups only small files, leaving large transfers on the
// normal streaming endpoint. Leftovers under 8 files remain individual requests.
func selectBatchChunks(candidates []batchFile) [][]batchFile {
	return selectBatchChunksWithTarget(candidates, maxBatchBytes)
}

func selectBatchChunksWithTarget(candidates []batchFile, target int64) [][]batchFile {
	if target <= 0 || target > maxBatchBytes {
		target = maxBatchBytes
	}
	var batches [][]batchFile
	var chunk []batchFile
	var size int64
	flush := func() {
		if len(chunk) >= minBatchEntries {
			batches = append(batches, append([]batchFile(nil), chunk...))
		}
		chunk = nil
		size = 0
	}
	for _, candidate := range candidates {
		if candidate.Info.Size > maxBatchFileBytes {
			continue
		}
		if len(chunk) >= maxBatchEntries || size+candidate.Info.Size > target {
			flush()
		}
		chunk = append(chunk, candidate)
		size += candidate.Info.Size
	}
	flush()
	return batches
}
