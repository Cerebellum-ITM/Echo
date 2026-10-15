package cmd

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type progressLine struct {
	msg    string
	fields map[string]string
}

func fakeProgressClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	orig := progressNow
	progressNow = func() time.Time { return now }
	t.Cleanup(func() { progressNow = orig })
	return &now
}

func recordingReporter(msg string, total int64) (*progressReporter, *[]progressLine) {
	var lines []progressLine
	p := newProgressReporter(func(msg string, fields ...[2]string) {
		m := map[string]string{}
		for _, f := range fields {
			m[f[0]] = f[1]
		}
		lines = append(lines, progressLine{msg, m})
	}, msg, total)
	return p, &lines
}

func TestProgressReporterBuckets(t *testing.T) {
	fakeProgressClock(t)
	p, lines := recordingReporter("restoring data", 1000)
	for _, n := range []int64{7, 93, 1, 250, 49, 300, 299, 1} {
		p.Add(n)
	}
	var got []string
	for _, l := range *lines {
		got = append(got, l.fields["progress"])
	}
	want := "10% 35% 40% 70% 99% 100%"
	if strings.Join(got, " ") != want {
		t.Fatalf("progress lines = %v, want %s", got, want)
	}
	last := (*lines)[len(*lines)-1]
	if last.fields["read"] != "1.0kB" || last.fields["total"] != "1.0kB" {
		t.Fatalf("last line fields = %v", last.fields)
	}
}

func TestProgressReporterHeartbeat(t *testing.T) {
	now := fakeProgressClock(t)
	p, lines := recordingReporter("restoring data", 1000)
	p.Add(50)

	*now = now.Add(29 * time.Second)
	p.beat()
	if len(*lines) != 0 {
		t.Fatalf("heartbeat before 30s: %v", *lines)
	}

	*now = now.Add(time.Second)
	p.beat()
	if len(*lines) != 1 {
		t.Fatalf("want one heartbeat at 30s, got %v", *lines)
	}
	if l := (*lines)[0]; l.msg != "restoring data" || l.fields["progress"] != "5%" || l.fields["elapsed"] != "30s" {
		t.Fatalf("heartbeat = %+v", l)
	}

	*now = now.Add(10 * time.Second)
	p.beat()
	if len(*lines) != 1 {
		t.Fatalf("heartbeat repeated before the next 30s: %v", *lines)
	}
}

func TestCountingReaderEOFSwitchesToFinalizing(t *testing.T) {
	now := fakeProgressClock(t)
	p, lines := recordingReporter("restoring data", 10)
	r := &countingReader{r: strings.NewReader("0123456789"), p: p}
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	r.Read(make([]byte, 1))

	finalizing := 0
	for _, l := range *lines {
		if l.msg == finalizingMsg {
			finalizing++
		}
	}
	if finalizing != 1 {
		t.Fatalf("finalizing emitted %d times: %v", finalizing, *lines)
	}

	*now = now.Add(time.Minute + 30*time.Second)
	p.beat()
	last := (*lines)[len(*lines)-1]
	if last.msg != finalizingMsg || last.fields["elapsed"] != "1m30s" || last.fields["progress"] != "" {
		t.Fatalf("heartbeat after EOF = %+v", last)
	}
}

func TestProgressReporterZeroTotalHeartbeatOnly(t *testing.T) {
	now := fakeProgressClock(t)
	p, lines := recordingReporter("copying filestore", 0)
	p.Add(5000)
	*now = now.Add(30 * time.Second)
	p.beat()
	if len(*lines) != 1 || (*lines)[0].fields["progress"] != "" || (*lines)[0].fields["elapsed"] != "30s" {
		t.Fatalf("lines = %v", *lines)
	}
}

func TestUnzipReportsProgress(t *testing.T) {
	fakeProgressClock(t)
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "backup.zip")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, size := range map[string]int{"dump.sql": 4000, "filestore/ab/abcdef": 6000} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(bytes.Repeat([]byte("x"), size))
	}
	zw.Close()
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	p, lines := recordingReporter("extracting archive", 0)
	if err := unzip(zipPath, filepath.Join(dir, "out"), p); err != nil {
		t.Fatal(err)
	}
	last := (*lines)[len(*lines)-1]
	if last.fields["progress"] != "100%" || last.fields["total"] != "10.0kB" {
		t.Fatalf("last line = %+v", last)
	}

	if err := unzip(zipPath, filepath.Join(dir, "plain"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "plain", "dump.sql")); err != nil {
		t.Fatal(err)
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{
		999:           "999B",
		1500:          "1.5kB",
		1500000:       "1.5MB",
		2800000000:    "2.8GB",
		4200000000000: "4200.0GB",
	} {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
