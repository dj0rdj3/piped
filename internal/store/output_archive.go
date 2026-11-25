package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

const (
	archiveChunkLimit = 64
	// A framed 256 KiB block needs less than this even for incompressible data.
	maxArchiveBytes = 512 * 1024
)

// CompressRunOutput replaces all raw output for a finished host with indexed
// gzip blocks. Each transaction moves at most 256 KiB, so other hosts can keep
// writing. Readers see either representation atomically with unchanged cursors.
// Interrupted compression is safe to resume, including after a service restart.
func (s *Store) CompressRunOutput(ctx context.Context, runID, hostID string) error {
	for {
		more, err := s.compressOutputBlock(ctx, runID, hostID)
		if err != nil || !more {
			return err
		}
	}
}

func (s *Store) compressOutputBlock(ctx context.Context, runID, hostID string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var finished int64
	err = tx.QueryRowContext(ctx, `SELECT finished_at FROM run_hosts WHERE run_id = ? AND host_id = ?`, runID, hostID).Scan(&finished)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if finished == 0 {
		return false, ErrRunActive
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, CASE WHEN LENGTH(data) BETWEEN 1 AND 4096 THEN data ELSE NULL END FROM run_output WHERE run_id = ? AND host_id = ? ORDER BY id LIMIT 64`, runID, hostID)
	if err != nil {
		return false, err
	}
	chunks := []OutputChunk{}
	for rows.Next() {
		var chunk OutputChunk
		if err := rows.Scan(&chunk.ID, &chunk.Data); err != nil {
			rows.Close()
			return false, err
		}
		chunks = append(chunks, chunk)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return false, err
	}
	if len(chunks) == 0 {
		return false, nil
	}
	data, err := encodeOutputBlock(chunks)
	if err != nil {
		return false, err
	}
	first, last := chunks[0].ID, chunks[len(chunks)-1].ID
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_output_archives(run_id, host_id, first_id, last_id, data) VALUES (?, ?, ?, ?, ?)`, runID, hostID, first, last, data); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM run_output WHERE run_id = ? AND host_id = ? AND id <= ?`, runID, hostID, last); err != nil {
		return false, err
	}
	return len(chunks) == archiveChunkLimit, tx.Commit()
}

// Maintenance also archives hosts interrupted by a restart and retries any
// incomplete compression without changing execution outcomes or replaying jobs.
func (s *Store) CompressCompletedOutput(ctx context.Context) error {
	var firstError error
	var afterRun, afterHost string
	for {
		var runID, hostID string
		err := s.db.QueryRowContext(ctx, `SELECT h.run_id, h.host_id FROM run_hosts h
			WHERE h.finished_at > 0 AND (h.run_id > ? OR (h.run_id = ? AND h.host_id > ?))
			AND EXISTS (SELECT 1 FROM run_output o WHERE o.run_id = h.run_id AND o.host_id = h.host_id)
			ORDER BY h.run_id, h.host_id LIMIT 1`, afterRun, afterRun, afterHost).Scan(&runID, &hostID)
		if errors.Is(err, sql.ErrNoRows) {
			return firstError
		}
		if err != nil {
			return err
		}
		if err := s.CompressRunOutput(ctx, runID, hostID); err != nil && !errors.Is(err, ErrNotFound) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// One damaged host must not block archiving every later completion.
			if firstError == nil {
				firstError = fmt.Errorf("run %s host %s: %w", runID, hostID, err)
			}
		}
		afterRun, afterHost = runID, hostID
	}
}

// Preserve original IDs and byte boundaries inside each independently readable
// block. Framing and decompression limits bound corrupt or oversized archives.
func encodeOutputBlock(chunks []OutputChunk) ([]byte, error) {
	if len(chunks) == 0 || len(chunks) > archiveChunkLimit {
		return nil, errors.New("invalid output archive chunk count")
	}
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	defer writer.Close()
	var previous int64
	for _, chunk := range chunks {
		if chunk.ID <= previous || len(chunk.Data) == 0 || len(chunk.Data) > 4096 {
			return nil, errors.New("invalid output archive chunk")
		}
		var header [12]byte
		binary.BigEndian.PutUint64(header[:8], uint64(chunk.ID))
		binary.BigEndian.PutUint32(header[8:], uint32(len(chunk.Data)))
		if _, err := writer.Write(header[:]); err != nil {
			return nil, err
		}
		if _, err := writer.Write(chunk.Data); err != nil {
			return nil, err
		}
		previous = chunk.ID
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func decodeOutputBlock(data []byte, first, last int64) ([]OutputChunk, error) {
	if len(data) > maxArchiveBytes {
		return nil, errors.New("output archive exceeds block size limit")
	}
	source := bytes.NewReader(data)
	reader, err := gzip.NewReader(source)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	// The encoder writes exactly one member. Reject appended members rather
	// than spending unbounded CPU traversing empty concatenated gzip streams.
	reader.Multistream(false)
	chunks := []OutputChunk{}
	var previous int64
	for {
		var header [12]byte
		_, err := io.ReadFull(reader, header[:])
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id := int64(binary.BigEndian.Uint64(header[:8]))
		size := binary.BigEndian.Uint32(header[8:])
		if len(chunks) == archiveChunkLimit || id <= previous || id < first || id > last || size == 0 || size > 4096 {
			return nil, errors.New("invalid output archive framing")
		}
		chunk := OutputChunk{ID: id, Data: make([]byte, int(size))}
		if _, err := io.ReadFull(reader, chunk.Data); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
		previous = id
	}
	if len(chunks) == 0 || chunks[0].ID != first || previous != last || source.Len() != 0 {
		return nil, errors.New("invalid output archive boundaries")
	}
	return chunks, nil
}

type outputRecord struct {
	first, last int64
	data        []byte
	compressed  bool
}

// Read both representations in one statement snapshot, including while a host
// is being archived. Limit database batches and inflate only the current block.
type outputReader struct {
	store         *Store
	ctx           context.Context
	runID, hostID string
	cursor        int64
	descending    bool
	inclusive     bool
	records       []outputRecord
	chunks        []OutputChunk
}

// Determine whether another page exists without inflating output outside the
// requested byte range. An older damaged archive must not hide a healthy tail.
func (r *outputReader) hasMore() (bool, error) {
	if err := r.ctx.Err(); err != nil {
		return false, err
	}
	if len(r.chunks) == 0 && len(r.records) == 0 {
		if err := r.load(); err != nil {
			return false, err
		}
	}
	return len(r.chunks) > 0 || len(r.records) > 0, nil
}

func (r *outputReader) next() (OutputChunk, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return OutputChunk{}, err
		}
		if len(r.chunks) > 0 {
			chunk := r.chunks[0]
			r.chunks = r.chunks[1:]
			return chunk, nil
		}
		if len(r.records) == 0 {
			if err := r.load(); err != nil {
				return OutputChunk{}, err
			}
			if len(r.records) == 0 {
				return OutputChunk{}, io.EOF
			}
		}
		record := r.records[0]
		r.records = r.records[1:]
		if record.compressed {
			chunks, err := decodeOutputBlock(record.data, record.first, record.last)
			if err != nil {
				// EOF from a damaged gzip stream is not the end of saved output.
				if errors.Is(err, io.EOF) {
					err = io.ErrUnexpectedEOF
				}
				return OutputChunk{}, fmt.Errorf("decode output archive: %w", err)
			}
			if r.descending {
				slices.Reverse(chunks)
			}
			for _, chunk := range chunks {
				pastCursor := chunk.ID > r.cursor
				if r.descending {
					pastCursor = chunk.ID < r.cursor || r.inclusive && chunk.ID == r.cursor
				}
				if r.cursor == 0 || pastCursor {
					r.chunks = append(r.chunks, chunk)
				}
			}
		} else {
			if len(record.data) == 0 || len(record.data) > 4096 {
				return OutputChunk{}, errors.New("invalid output chunk size")
			}
			r.chunks = []OutputChunk{{ID: record.first, Data: record.data}}
		}
		if r.descending {
			r.cursor = record.first
		} else {
			r.cursor = record.last
		}
		r.inclusive = false
	}
}

func (r *outputReader) load() error {
	comparison, order, archiveID := ">", "ASC", "last_id"
	if r.descending {
		comparison, order, archiveID = "<", "DESC", "first_id"
		if r.inclusive {
			comparison = "<="
		}
	}
	rawFilter, archiveFilter := "", ""
	arguments := []any{r.runID, r.hostID}
	if r.cursor != 0 {
		rawFilter = " AND id " + comparison + " ?"
		arguments = append(arguments, r.cursor)
	}
	arguments = append(arguments, maxArchiveBytes, r.runID, r.hostID)
	if r.cursor != 0 {
		archiveFilter = " AND " + archiveID + " " + comparison + " ?"
		arguments = append(arguments, r.cursor)
	}
	// Limit each branch before UNION: MySQL/MariaDB may otherwise materialize
	// the whole log before applying the final LIMIT. Prefetch just one archive
	// block, keeping memory bounded even when stored output is incompressible.
	query := `SELECT * FROM (
		SELECT id AS first_id, id AS last_id,
		CASE WHEN LENGTH(data) BETWEEN 1 AND 4096 THEN data ELSE NULL END AS data,
		0 AS compressed FROM run_output WHERE run_id = ? AND host_id = ?` + rawFilter +
		` ORDER BY id ` + order + ` LIMIT 16) raw_output
		UNION ALL SELECT * FROM (
		SELECT first_id, last_id,
		CASE WHEN LENGTH(data) <= ? THEN data ELSE NULL END AS data,
		1 AS compressed FROM run_output_archives WHERE run_id = ? AND host_id = ?` + archiveFilter +
		` ORDER BY ` + archiveID + ` ` + order + ` LIMIT 1) archived_output` +
		` ORDER BY ` + archiveID + ` ` + order + ` LIMIT 16`
	rows, err := r.store.db.QueryContext(r.ctx, query, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record outputRecord
		if err := rows.Scan(&record.first, &record.last, &record.data, &record.compressed); err != nil {
			return err
		}
		r.records = append(r.records, record)
	}
	return rows.Err()
}
