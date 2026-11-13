package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CopyMode selects how data is moved into a destination that may already hold data.
type CopyMode string

const (
	// CopyMerge keeps the destination's own channels and videos and upserts the
	// source's by their YouTube IDs. See MergeDatabase.
	CopyMerge CopyMode = "merge"
	// CopyReplace deletes everything in the destination and copies the source
	// verbatim, primary keys included. See CopyDatabase.
	CopyReplace CopyMode = "replace"
)

// ParseCopyMode accepts "merge" (the default when empty) or "replace".
func ParseCopyMode(s string) (CopyMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(CopyMerge):
		return CopyMerge, nil
	case string(CopyReplace):
		return CopyReplace, nil
	default:
		return "", fmt.Errorf("unknown copy mode %q (use merge or replace)", s)
	}
}

// Transfer moves src's data into dst using mode.
func Transfer(src, dst *Storage, mode CopyMode) (CopyResult, error) {
	if mode == CopyReplace {
		return CopyDatabase(src, dst, true)
	}
	return MergeDatabase(src, dst)
}

// statusRank orders a video's lifecycle. Merging never moves a video backwards, so the
// "never redownload a handled video" guarantee (see AddPrunedVideo) survives merging an
// older copy of the data over a newer one, in either direction.
var statusRank = map[string]int{"pending": 0, "downloaded": 1, "pruned": 2}

// MergeDatabase upserts every channel and video from src into dst, matching them by
// their YouTube IDs, so it works whatever dst already holds. Channels and videos that
// exist only in dst are kept. For ones in both, src's settings, titles, and channel
// links win, except that a video's status never regresses (pending < downloaded <
// pruned). A video is either channel-owned or individually tracked, never both; src's
// side wins. Everything runs in one dst transaction, so a failure changes nothing.
func MergeDatabase(src, dst *Storage) (CopyResult, error) {
	return mergeDatabase(src, dst, false)
}

// ReconcileInto is the conservative merge used to fold data written to a temporary
// fallback database back into the main one: rows that dst lacks are added, and a video
// dst already has only ever advances its status (pending < downloaded < pruned).
// Nothing dst already holds is overwritten or relinked.
func ReconcileInto(src, dst *Storage) (CopyResult, error) {
	return mergeDatabase(src, dst, true)
}

func mergeDatabase(src, dst *Storage, keepExisting bool) (CopyResult, error) {
	var result CopyResult
	if err := checkSameSchema(src, dst); err != nil {
		return result, err
	}

	tables := map[string]tableRows{}
	for _, t := range copyTables {
		rows, err := readTable(src.db, t)
		if err != nil {
			return result, fmt.Errorf("reading source table %s: %w", t, err)
		}
		tables[t] = rows
	}

	tx, err := dst.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	add := func(tc TableCopyCount) { result.Tables = append(result.Tables, tc) }

	// sources: seeded identically by migration 0001; only add ids dst lacks.
	srcSources := tables["sources"]
	sc := TableCopyCount{Table: "sources"}
	for _, row := range srcSources.rows {
		res, err := tx.Exec(insertSQL("sources", srcSources.cols)+` ON CONFLICT DO NOTHING`, row...)
		if err != nil {
			return result, fmt.Errorf("merging sources: %w", err)
		}
		sc.Rows++
		if n, _ := res.RowsAffected(); n > 0 {
			sc.Inserted++
		}
	}
	add(sc)

	channelMap, _, chCount, csCount, err := mergeEntities(tx, tables["channels"], tables["channel_sources"], "channel", keepExisting)
	if err != nil {
		return result, err
	}
	add(chCount)
	add(csCount)

	videoMap, existingVideos, vCount, vsCount, err := mergeEntities(tx, tables["videos"], tables["video_sources"], "video", keepExisting)
	if err != nil {
		return result, err
	}
	add(vCount)
	add(vsCount)

	cv := tables["channel_videos"]
	cvCount := TableCopyCount{Table: "channel_videos"}
	for _, row := range cv.rows {
		dc, okC := channelMap[toInt64(row[cv.idx("channel_id")])]
		dv, okV := videoMap[toInt64(row[cv.idx("video_id")])]
		if !okC || !okV || (keepExisting && existingVideos[dv]) {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM individual_video_tracking WHERE video_id = ?`, dv); err != nil {
			return result, err
		}
		res, err := tx.Exec(`DELETE FROM channel_videos WHERE video_id = ?`, dv)
		if err != nil {
			return result, err
		}
		if _, err := tx.Exec(`INSERT INTO channel_videos (channel_id, video_id) VALUES (?, ?)`, dc, dv); err != nil {
			return result, fmt.Errorf("linking video: %w", err)
		}
		cvCount.Rows++
		if n, _ := res.RowsAffected(); n > 0 {
			cvCount.Updated++
		} else {
			cvCount.Inserted++
		}
	}
	add(cvCount)

	ivt := tables["individual_video_tracking"]
	ivtCount := TableCopyCount{Table: "individual_video_tracking"}
	for _, row := range ivt.rows {
		dv, ok := videoMap[toInt64(row[ivt.idx("video_id")])]
		if !ok || (keepExisting && existingVideos[dv]) {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM channel_videos WHERE video_id = ?`, dv); err != nil {
			return result, err
		}
		res, err := tx.Exec(`DELETE FROM individual_video_tracking WHERE video_id = ?`, dv)
		if err != nil {
			return result, err
		}
		values := append([]any(nil), row...)
		values[ivt.idx("video_id")] = dv
		if _, err := tx.Exec(insertSQL("individual_video_tracking", ivt.cols), values...); err != nil {
			return result, fmt.Errorf("tracking video: %w", err)
		}
		ivtCount.Rows++
		if n, _ := res.RowsAffected(); n > 0 {
			ivtCount.Updated++
		} else {
			ivtCount.Inserted++
		}
	}
	add(ivtCount)

	// json_import_state: once either side imported the legacy file, it stays imported.
	js := tables["json_import_state"]
	jsCount := TableCopyCount{Table: "json_import_state"}
	for _, row := range js.rows {
		if toInt64(row[js.idx("imported")]) == 0 {
			continue
		}
		if _, err := tx.Exec(`UPDATE json_import_state SET imported = 1, imported_at = ?, source_path = ? WHERE id = 1 AND imported = 0`,
			row[js.idx("imported_at")], row[js.idx("source_path")]); err != nil {
			return result, err
		}
		jsCount.Rows++
	}
	add(jsCount)

	if err := tx.Commit(); err != nil {
		return result, err
	}
	dst.notify()
	return result, nil
}

// mergeEntities upserts channels or videos (the core table) together with their
// per-source identity rows (channel_sources / video_sources), matching on (source_id,
// source_<kind>_id). It returns a map from src primary keys to dst primary keys and the
// set of dst keys that already existed. With keepExisting, existing rows are left as
// they are, except that a video's status may advance.
func mergeEntities(tx *dbTx, core, sources tableRows, kind string, keepExisting bool) (map[int64]int64, map[int64]bool, TableCopyCount, TableCopyCount, error) {
	coreTable, sourcesTable := kind+"s", kind+"_sources"
	fk := kind + "_id"
	natural := "source_" + kind + "_id"
	coreCount := TableCopyCount{Table: coreTable}
	srcCount := TableCopyCount{Table: sourcesTable}
	pkMap := map[int64]int64{}
	existing := map[int64]bool{}

	coreByID := make(map[int64][]any, len(core.rows))
	for _, row := range core.rows {
		coreByID[toInt64(row[core.idx("id")])] = row
	}
	coreCols := without(core.cols, "id")
	srcUpdateCols := without(sources.cols, fk, "source_id", natural)

	for _, srow := range sources.rows {
		srcPK := toInt64(srow[sources.idx(fk)])
		crow, ok := coreByID[srcPK]
		if !ok {
			continue // an identity row with no core row can't exist under the FKs
		}
		coreValues := pick(core, crow, coreCols)

		var dstPK int64
		err := tx.QueryRow(`SELECT `+fk+` FROM `+sourcesTable+` WHERE source_id = ? AND `+natural+` = ?`,
			srow[sources.idx("source_id")], srow[sources.idx(natural)]).Scan(&dstPK)
		switch {
		case err == sql.ErrNoRows:
			if err := tx.QueryRow(insertSQL(coreTable, coreCols)+` RETURNING id`, coreValues...).Scan(&dstPK); err != nil {
				return nil, nil, coreCount, srcCount, fmt.Errorf("inserting %s: %w", kind, err)
			}
			values := append([]any(nil), srow...)
			values[sources.idx(fk)] = dstPK
			if _, err := tx.Exec(insertSQL(sourcesTable, sources.cols), values...); err != nil {
				return nil, nil, coreCount, srcCount, fmt.Errorf("inserting %s: %w", sourcesTable, err)
			}
			coreCount.Inserted++
			srcCount.Inserted++
		case err != nil:
			return nil, nil, coreCount, srcCount, err
		case keepExisting:
			existing[dstPK] = true
			if kind == "video" {
				advanced, err := advanceStatus(tx, dstPK, core, coreCols, coreValues)
				if err != nil {
					return nil, nil, coreCount, srcCount, err
				}
				if advanced {
					coreCount.Updated++
				}
			}
		default:
			existing[dstPK] = true
			if kind == "video" {
				if err := keepFurthestStatus(tx, dstPK, core, coreCols, coreValues); err != nil {
					return nil, nil, coreCount, srcCount, err
				}
			}
			if _, err := tx.Exec(updateSQL(coreTable, coreCols, "id = ?"), append(coreValues, dstPK)...); err != nil {
				return nil, nil, coreCount, srcCount, fmt.Errorf("updating %s: %w", kind, err)
			}
			if len(srcUpdateCols) > 0 {
				args := append(pick(sources, srow, srcUpdateCols), srow[sources.idx("source_id")], srow[sources.idx(natural)])
				if _, err := tx.Exec(updateSQL(sourcesTable, srcUpdateCols, "source_id = ? AND "+natural+" = ?"), args...); err != nil {
					return nil, nil, coreCount, srcCount, fmt.Errorf("updating %s: %w", sourcesTable, err)
				}
			}
			coreCount.Updated++
			srcCount.Updated++
		}
		coreCount.Rows++
		srcCount.Rows++
		pkMap[srcPK] = dstPK
	}
	return pkMap, existing, coreCount, srcCount, nil
}

// advanceStatus moves the dst video to src's status when src's is further along.
func advanceStatus(tx *dbTx, dstPK int64, core tableRows, cols []string, values []any) (bool, error) {
	statusAt, downloadAt := indexOf(cols, "status"), indexOf(cols, "download_date")
	if statusAt < 0 {
		return false, nil
	}
	var dstStatus string
	if err := tx.QueryRow(`SELECT status FROM videos WHERE id = ?`, dstPK).Scan(&dstStatus); err != nil {
		return false, err
	}
	srcStatus, _ := values[statusAt].(string)
	if statusRank[srcStatus] <= statusRank[dstStatus] {
		return false, nil
	}
	var download any
	if downloadAt >= 0 {
		download = values[downloadAt]
	}
	_, err := tx.Exec(`UPDATE videos SET status = ?, download_date = COALESCE(?, download_date) WHERE id = ?`, srcStatus, download, dstPK)
	return err == nil, err
}

// keepFurthestStatus rewrites values in place so an update never moves the dst video's
// status backwards, keeping dst's status and download date when it is further along.
func keepFurthestStatus(tx *dbTx, dstPK int64, core tableRows, cols []string, values []any) error {
	var dstStatus string
	var dstDownload sql.NullString
	if err := tx.QueryRow(`SELECT status, download_date FROM videos WHERE id = ?`, dstPK).Scan(&dstStatus, &dstDownload); err != nil {
		return err
	}
	statusAt, downloadAt := indexOf(cols, "status"), indexOf(cols, "download_date")
	if statusAt < 0 {
		return nil
	}
	srcStatus, _ := values[statusAt].(string)
	if statusRank[dstStatus] <= statusRank[srcStatus] {
		return nil
	}
	values[statusAt] = dstStatus
	if downloadAt >= 0 && dstDownload.Valid {
		values[downloadAt] = dstDownload.String
	}
	return nil
}

func checkSameSchema(src, dst *Storage) error {
	srcVersion, err := schemaVersion(src.db)
	if err != nil {
		return fmt.Errorf("reading source schema version: %w", err)
	}
	dstVersion, err := schemaVersion(dst.db)
	if err != nil {
		return fmt.Errorf("reading destination schema version: %w", err)
	}
	if srcVersion != dstVersion {
		return fmt.Errorf("schema versions differ (source %d, destination %d); open both with the same ytdm version", srcVersion, dstVersion)
	}
	return nil
}

// tableRows is a whole table read into memory, values normalized to int64, string, or
// nil (every column is INTEGER or TEXT on both backends).
type tableRows struct {
	cols []string
	rows [][]any
}

func (t tableRows) idx(col string) int {
	return indexOf(t.cols, col)
}

func readTable(db *dbConn, table string) (tableRows, error) {
	var t tableRows
	rows, err := db.Query(`SELECT * FROM ` + table)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	if t.cols, err = rows.Columns(); err != nil {
		return t, err
	}
	for rows.Next() {
		values := make([]any, len(t.cols))
		ptrs := make([]any, len(t.cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return t, err
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}
		t.rows = append(t.rows, values)
	}
	return t, rows.Err()
}

func insertSQL(table string, cols []string) string {
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
	return fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, table, strings.Join(cols, ", "), placeholders)
}

func updateSQL(table string, cols []string, where string) string {
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = c + " = ?"
	}
	return fmt.Sprintf(`UPDATE %s SET %s WHERE %s`, table, strings.Join(sets, ", "), where)
}

func indexOf(cols []string, col string) int {
	for i, c := range cols {
		if c == col {
			return i
		}
	}
	return -1
}

func without(cols []string, drop ...string) []string {
	var out []string
	for _, c := range cols {
		if indexOf(drop, c) < 0 {
			out = append(out, c)
		}
	}
	return out
}

func pick(t tableRows, row []any, cols []string) []any {
	out := make([]any, len(cols))
	for i, c := range cols {
		out[i] = row[t.idx(c)]
	}
	return out
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case string:
		var i int64
		fmt.Sscan(n, &i)
		return i
	}
	return 0
}

// oldBackendGrace is how long a replaced backend stays open after MigrateTo, so reads
// that started before the switch can finish.
var oldBackendGrace = 2 * time.Minute

// MigrateTo moves this live Storage onto another database: it opens target, stops all
// writes, transfers the data with mode, then switches every future query to target.
// Callers keep using the same *Storage. If anything fails, nothing is switched and the
// current database is untouched.
//
// beforeSwitch, when non-nil, runs after the data is safely in target but before the
// switch (e.g. to record target as the database to open on the next start); if it
// fails, nothing is switched.
func (s *Storage) MigrateTo(target Options, mode CopyMode, beforeSwitch func() error) (CopyResult, error) {
	dst, err := Open(target)
	if err != nil {
		return CopyResult{}, fmt.Errorf("opening target: %w", err)
	}
	switched := false
	defer func() {
		if !switched {
			dst.Close()
		}
	}()

	// Hold the write lock for the whole copy: the source can't change underneath it,
	// and writers that queue up meanwhile land on the new database once released.
	s.db.writeMu.Lock()
	defer s.db.writeMu.Unlock()

	result, err := Transfer(s, dst, mode)
	if err != nil {
		return result, err
	}
	if beforeSwitch != nil {
		if err := beforeSwitch(); err != nil {
			return result, err
		}
	}

	old := s.db.cur.Swap(dst.db.backend())
	switched = true
	time.AfterFunc(oldBackendGrace, func() { old.db.Close() })
	s.notify()
	return result, nil
}

// IsEmpty reports whether the database tracks no channels and no videos.
func (s *Storage) IsEmpty() (bool, error) {
	var n int64
	err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM channels) + (SELECT COUNT(*) FROM videos)`).Scan(&n)
	return n == 0, err
}
