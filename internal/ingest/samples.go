package ingest

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/mehdashti/limit-ledger/internal/sample"
	"github.com/mehdashti/limit-ledger/internal/store"
)

// SampleStats describes one pass over the spool file.
type SampleStats struct {
	Read, Kept int
}

// keepEvery keeps one unchanged sample per interval, so a long session with steady
// figures does not fill the database.
const keepEvery = 10 * 60 * 1000 // ms

// Samples moves new lines of the statusline spool file into the database. A sample
// is kept when any figure changed since the last kept one, or keepEvery has passed.
func Samples(st *store.Store, spool string) (SampleStats, error) {
	var stats SampleStats
	f, err := os.Open(spool)
	if errors.Is(err, fs.ErrNotExist) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return stats, err
	}
	_, ino := fileID(fi)
	offset, _, err := st.MetaInt("spool_offset")
	if err != nil {
		return stats, err
	}
	prevIno, _, err := st.MetaInt("spool_ino")
	if err != nil {
		return stats, err
	}
	if uint64(prevIno) != ino || fi.Size() < offset {
		offset = 0 // the spool was replaced or truncated
	}
	if fi.Size() == offset {
		return stats, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return stats, err
	}
	last, err := st.LastSample()
	if err != nil {
		return stats, err
	}
	b, err := st.Begin()
	if err != nil {
		return stats, err
	}
	var putErr error
	consumed, err := readLines(bufio.NewReaderSize(f, 1<<16), func(line []byte) {
		var s sample.Sample
		if json.Unmarshal(line, &s) != nil || s.TS == 0 {
			return
		}
		stats.Read++
		if last != nil && same(*last, s) && s.TS-last.TS < keepEvery {
			return
		}
		if putErr == nil {
			putErr = b.PutSample(s)
		}
		stats.Kept++
		c := s
		last = &c
	})
	if err == nil {
		err = putErr
	}
	if err == nil {
		err = b.SetMeta("spool_offset", offset+consumed)
	}
	if err == nil {
		err = b.SetMeta("spool_ino", int64(ino))
	}
	if err != nil {
		b.Rollback()
		return stats, err
	}
	return stats, b.Commit()
}

func same(a, b sample.Sample) bool {
	eqF := func(x, y *float64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	eqI := func(x, y *int64) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return eqF(a.SevenPct, b.SevenPct) && eqF(a.FivePct, b.FivePct) &&
		eqI(a.SevenReset, b.SevenReset) && eqI(a.FiveReset, b.FiveReset)
}
