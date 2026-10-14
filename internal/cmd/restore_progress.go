package cmd

import (
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"
)

var (
	progressNow       = time.Now
	progressHeartbeat = 30 * time.Second
	progressTick      = time.Second
)

const finalizingMsg = "finalizing (indexes and constraints)"

// progressReporter turns a byte count into INFO progress lines: one per
// 10% bucket crossed, plus a heartbeat with the elapsed time whenever
// progressHeartbeat passes without a line, so a long restore never goes
// silent. A zero total keeps only the heartbeat. A nil *progressReporter
// is valid and does nothing.
type progressReporter struct {
	emit  func(msg string, fields ...[2]string)
	start time.Time

	mu       sync.Mutex
	msg      string
	total    int64
	read     int64
	bucket   int64
	lastEmit time.Time
	eof      bool

	done chan struct{}
	wg   sync.WaitGroup
}

func newProgressReporter(emit func(msg string, fields ...[2]string), msg string, total int64) *progressReporter {
	now := progressNow()
	return &progressReporter{emit: emit, msg: msg, total: total, start: now, lastEmit: now, done: make(chan struct{})}
}

// newRestoreProgress returns a started reporter that logs under
// echo.<cmd>.restore for target, or nil when opts has no logger.
func newRestoreProgress(opts DBOpts, target, msg string, total int64) *progressReporter {
	if opts.Log == nil {
		return nil
	}
	p := newProgressReporter(func(msg string, fields ...[2]string) {
		opts.log("INFO", "restore", msg, target, fields...)
	}, msg, total)
	p.Start()
	return p
}

func (p *progressReporter) Start() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		t := time.NewTicker(progressTick)
		defer t.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-t.C:
				p.beat()
			}
		}
	}()
}

func (p *progressReporter) Stop() {
	if p == nil {
		return
	}
	close(p.done)
	p.wg.Wait()
}

// setTotal sets the byte total once it is known (unzip reads it from the
// archive after the reporter has started).
func (p *progressReporter) setTotal(total int64) {
	p.mu.Lock()
	p.total = total
	p.mu.Unlock()
}

func (p *progressReporter) Add(n int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.read += n
	if p.total <= 0 {
		return
	}
	if b := p.percent() / 10; b > p.bucket {
		p.bucket = b
		p.emitLocked(p.msg,
			[2]string{"progress", strconv.FormatInt(p.percent(), 10) + "%"},
			[2]string{"read", formatBytes(p.read)},
			[2]string{"total", formatBytes(p.total)})
	}
}

// EOF marks the input as fully consumed: the restore process keeps
// working on indexes and constraints, so the heartbeat switches to the
// finalizing message from here on.
func (p *progressReporter) EOF() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.eof {
		return
	}
	p.eof = true
	p.msg = finalizingMsg
	p.emitLocked(p.msg, [2]string{"elapsed", p.elapsed()})
}

func (p *progressReporter) beat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if progressNow().Sub(p.lastEmit) < progressHeartbeat {
		return
	}
	var fields [][2]string
	if !p.eof && p.total > 0 {
		fields = append(fields, [2]string{"progress", strconv.FormatInt(p.percent(), 10) + "%"})
	}
	fields = append(fields, [2]string{"elapsed", p.elapsed()})
	p.emitLocked(p.msg, fields...)
}

func (p *progressReporter) emitLocked(msg string, fields ...[2]string) {
	p.lastEmit = progressNow()
	p.emit(msg, fields...)
}

func (p *progressReporter) percent() int64 {
	return min(p.read*100/p.total, 100)
}

func (p *progressReporter) elapsed() string {
	return progressNow().Sub(p.start).Round(time.Second).String()
}

type countingReader struct {
	r io.Reader
	p *progressReporter
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.p.Add(int64(n))
	if err == io.EOF {
		c.p.EOF()
	}
	return n, err
}

type countingWriter struct {
	w io.Writer
	p *progressReporter
}

func (c countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.p.Add(int64(n))
	return n, err
}

// formatBytes renders n in decimal units with one decimal and no space
// (1.5MB), so it stays a single key=value token in the log line.
func formatBytes(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10) + "B"
	}
	v, unit := float64(n)/1000, "kB"
	for _, next := range []string{"MB", "GB"} {
		if v < 1000 {
			break
		}
		v, unit = v/1000, next
	}
	return fmt.Sprintf("%.1f%s", v, unit)
}
