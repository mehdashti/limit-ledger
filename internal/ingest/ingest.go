// Package ingest reads Claude Code transcripts into the ledger. The first run reads
// all history. Later runs read only what was appended since the last run.
package ingest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mehdashti/limit-ledger/internal/store"
	"github.com/mehdashti/limit-ledger/internal/transcript"
)

// Options configures a run.
type Options struct {
	Root    string // the projects directory, usually ~/.claude/projects
	Workers int    // parallel file readers; 0 means the number of CPUs
}

// Stats describes a run.
type Stats struct {
	Files       int   // transcript files found
	FilesRead   int   // files that had new data
	Bytes       int64 // bytes of complete lines read
	Lines       int64
	Responses   int64 // response records parsed (before de-duplication)
	Anchors     int64
	OrgEvents   int64 // credential_org records: which account was active
	ParseErrors int64
	Elapsed     time.Duration
}

type job struct {
	path, rel, key string
	from           int64
	state          store.FileState
}

type result struct {
	job         job
	offset      int64
	lines       int64
	parseErrors int64
	responses   []*transcript.Response
	anchors     []*transcript.Anchor
	orgs        []*transcript.OrgEvent
	err         error
}

// Run ingests every *.jsonl file under opt.Root.
func Run(st *store.Store, opt Options) (Stats, error) {
	start := time.Now()
	var stats Stats
	if fi, err := os.Stat(opt.Root); err != nil {
		return stats, err
	} else if !fi.IsDir() {
		return stats, fmt.Errorf("%s is not a directory", opt.Root)
	}
	known, err := st.FileStates()
	if err != nil {
		return stats, err
	}

	var jobs []job
	walkErr := filepath.WalkDir(opt.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == opt.Root {
				return err
			}
			return nil // unreadable entry: skip it, keep walking
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(opt.Root, p)
		if err != nil {
			return nil
		}
		stats.Files++
		dev, ino := fileID(info)
		cur := store.FileState{Dev: dev, Ino: ino, Size: info.Size(), Mtime: info.ModTime().UnixNano()}
		key := st.PathKey(rel)
		from := int64(0)
		if prev, ok := known[key]; ok && prev.Dev == dev && prev.Ino == ino && info.Size() >= prev.Offset {
			if info.Size() == prev.Offset {
				return nil // nothing new
			}
			from = prev.Offset
		}
		jobs = append(jobs, job{path: p, rel: rel, key: key, from: from, state: cur})
		return nil
	})
	if walkErr != nil {
		return stats, walkErr
	}
	// Largest first, so one big file does not finish last on its own.
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].state.Size-jobs[i].from > jobs[j].state.Size-jobs[j].from
	})

	workers := opt.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	jobCh := make(chan job)
	resCh := make(chan result, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				resCh <- readFile(j)
			}
		}()
	}
	go func() {
		for _, j := range jobs {
			jobCh <- j
		}
		close(jobCh)
		wg.Wait()
		close(resCh)
	}()

	writeErr := write(st, resCh, &stats)
	stats.Elapsed = time.Since(start)
	return stats, writeErr
}

// write is the single writer: it commits results in batches. A file's new offset is
// written in the same transaction as its rows, so a crash never skips data.
func write(st *store.Store, resCh <-chan result, stats *Stats) error {
	const maxRows, maxFiles = 50000, 500
	var (
		b          *store.Batch
		rows, nf   int
		firstErr   error
		readErrors []string
	)
	flush := func() error {
		if b == nil {
			return nil
		}
		err := b.Commit()
		b, rows, nf = nil, 0, 0
		return err
	}
	for r := range resCh {
		if firstErr != nil {
			continue // drain
		}
		if r.err != nil {
			readErrors = append(readErrors, fmt.Sprintf("%s: %v", r.job.rel, r.err))
		}
		if r.offset == r.job.from {
			continue // no complete new line yet (or nothing readable)
		}
		if b == nil {
			var err error
			if b, err = st.Begin(); err != nil {
				firstErr = err
				continue
			}
		}
		if err := apply(b, r); err != nil {
			b.Rollback()
			b = nil
			firstErr = err
			continue
		}
		stats.FilesRead++
		stats.Bytes += r.offset - r.job.from
		stats.Lines += r.lines
		stats.Responses += int64(len(r.responses))
		stats.Anchors += int64(len(r.anchors))
		stats.OrgEvents += int64(len(r.orgs))
		stats.ParseErrors += r.parseErrors
		rows += len(r.responses)
		nf++
		if rows >= maxRows || nf >= maxFiles {
			if err := flush(); err != nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		if b != nil {
			b.Rollback()
		}
		return firstErr
	}
	if err := flush(); err != nil {
		return err
	}
	if len(readErrors) > 0 {
		return fmt.Errorf("%d file(s) could not be read fully, first: %s", len(readErrors), readErrors[0])
	}
	return nil
}

func apply(b *store.Batch, r result) error {
	for _, resp := range r.responses {
		if err := b.PutResponse(resp); err != nil {
			return err
		}
	}
	for _, a := range r.anchors {
		if err := b.PutAnchor(a); err != nil {
			return err
		}
	}
	for _, e := range r.orgs {
		if err := b.PutOrgEvent(e); err != nil {
			return err
		}
	}
	s := r.job.state
	s.Offset = r.offset
	return b.PutFile(r.job.key, s)
}

func readFile(j job) result {
	res := result{job: j, offset: j.from}
	f, err := os.Open(j.path)
	if err != nil {
		res.err = err
		return res
	}
	defer f.Close()
	if j.from > 0 {
		if _, err := f.Seek(j.from, io.SeekStart); err != nil {
			res.err = err
			return res
		}
	}
	kind := transcript.KindFromPath(j.rel)
	consumed, err := readLines(bufio.NewReaderSize(f, 1<<20), func(line []byte) {
		res.lines++
		out, perr := transcript.ParseLine(line, kind)
		if perr != nil {
			res.parseErrors++
			return
		}
		if out.Response != nil {
			res.responses = append(res.responses, out.Response)
		}
		if out.Anchor != nil {
			res.anchors = append(res.anchors, out.Anchor)
		}
		if out.Org != nil {
			res.orgs = append(res.orgs, out.Org)
		}
	})
	res.offset = j.from + consumed
	res.err = err
	return res
}

// readLines calls fn for every complete, newline-terminated line and returns the
// number of bytes those lines took. A trailing line without a newline is still being
// written, so it is left for the next run. The slice passed to fn is reused.
func readLines(br *bufio.Reader, fn func([]byte)) (int64, error) {
	var consumed int64
	var acc []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			acc = append(acc, chunk...)
			continue
		}
		if errors.Is(err, io.EOF) {
			return consumed, nil
		}
		if err != nil {
			return consumed, err
		}
		line := chunk
		if len(acc) > 0 {
			acc = append(acc, chunk...)
			line = acc
		}
		consumed += int64(len(line))
		fn(line)
		acc = acc[:0]
	}
}
