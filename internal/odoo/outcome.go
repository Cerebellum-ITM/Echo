package odoo

import (
	"regexp"
	"strconv"
	"strings"
)

// testTally matches the report Odoo logs once per database at the end of a
// --stop-after-init run with tests enabled (odoo/service/server.py, the same
// on 17, 18 and 19; the text is OdooTestResult.__str__):
//
//	… odoo.tests.result: 1 failed, 0 error(s) of 12 tests when loading database 'x'
//
// The level is INFO, WARNING when no test ran and ERROR when one failed.
var testTally = regexp.MustCompile(`odoo\.tests\.(?:result|stats): (\d+) failed, (\d+) error\(s\) of (\d+) tests`)

// TestTally is the count of one Odoo test report.
type TestTally struct {
	Run    int `json:"run"`
	Failed int `json:"failed"`
	Errors int `json:"errors"`
}

// ParseTestTally reads a test report line; ok is false for any other line.
func ParseTestTally(line string) (t TestTally, ok bool) {
	m := testTally.FindStringSubmatch(line)
	if m == nil {
		return TestTally{}, false
	}
	t.Failed, _ = strconv.Atoi(m[1])
	t.Errors, _ = strconv.Atoi(m[2])
	t.Run, _ = strconv.Atoi(m[3])
	return t, true
}

// logHeader is the netsvc format shared by 17, 18 and 19:
// `%(asctime)s %(pid)s %(levelname)s %(dbname)s %(name)s: %(message)s %(perf_info)s`.
var logHeader = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3}) \d+ (DEBUG|INFO|WARNING|ERROR|CRITICAL) (\S+) (\S+?): (.*)$`)

var (
	traceFrame = regexp.MustCompile(`^\s+File "([^"]+)", line (\d+), in (.+)$`)
	// OdooTestResult.logError: "%s: %s\n%s" % (flavour, getDescription(test), traceback).
	// The description is `Class.method`, `Subtest Class.method (params)` or,
	// for a class or module fixture, `setUpClass (odoo.addons.m.tests.f.Class)`.
	testFailureHead = regexp.MustCompile(`^(FAIL|ERROR): (?:Subtest )?(\S+)(?: \((.*)\))?$`)
	addonModule     = regexp.MustCompile(`^odoo\.addons\.([^.]+)\.`)
)

// ansiSeq matches the SGR codes Odoo's ColoredFormatter adds when its output
// is a TTY; `compose logs` replays them as stored.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

const (
	maxWarnings     = 50
	maxExceptionLen = 300
)

// Frame is one traceback frame.
type Frame struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Func string `json:"func"`
}

// LogError is an ERROR or CRITICAL log record with its traceback.
type LogError struct {
	Time      string  `json:"time"`
	DB        string  `json:"db"`
	Logger    string  `json:"logger"`
	Message   string  `json:"message"`
	Exception string  `json:"exception"`
	Frames    []Frame `json:"frames"`
	// Lines is the record as logged: the header and its traceback.
	Lines []string `json:"-"`
}

// LogWarning is a WARNING log record.
type LogWarning struct {
	Time    string `json:"time"`
	DB      string `json:"db"`
	Logger  string `json:"logger"`
	Message string `json:"message"`
}

// TestFailure is one failing or erroring test. File and Line point at the
// deepest frame inside the test's module.
type TestFailure struct {
	Kind    string `json:"kind"`
	Test    string `json:"test"`
	Module  string `json:"module"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// Outcome reads an Odoo log stream line by line and keeps what an agent
// needs to know why a run failed: errors with their tracebacks, warnings,
// the test tally and each failing test. Test failures and the tally are not
// repeated among the errors. The zero value is ready to use.
type Outcome struct {
	Errors            []*LogError
	Warnings          []LogWarning
	WarningsTruncated int
	Tests             TestTally
	Tallied           bool
	Failures          []*TestFailure

	open    *LogError
	failure *TestFailure
	// excDone is set once the exception line after the last frame is read,
	// so a trailing detail line (psycopg2's DETAIL:) does not replace it.
	excDone bool
}

// Feed consumes one log line; ANSI color codes are ignored.
func (o *Outcome) Feed(line string) {
	if strings.Contains(line, "\x1b") {
		line = ansiSeq.ReplaceAllString(line, "")
	}
	m := logHeader.FindStringSubmatch(line)
	if m == nil {
		o.continueEntry(line)
		return
	}
	o.open, o.failure, o.excDone = nil, nil, false
	at, level, db, logger, msg := m[1], m[2], m[3], m[4], strings.TrimSpace(m[5])

	if t, ok := ParseTestTally(line); ok {
		o.Tests.Run += t.Run
		o.Tests.Failed += t.Failed
		o.Tests.Errors += t.Errors
		o.Tallied = true
		return
	}
	switch level {
	case "ERROR", "CRITICAL":
		o.open = &LogError{Time: at, DB: db, Logger: logger, Message: msg, Frames: []Frame{}, Lines: []string{line}}
		if f := testFailureHead.FindStringSubmatch(msg); f != nil {
			o.failure = newTestFailure(f[1], f[2], f[3], logger)
			o.Failures = append(o.Failures, o.failure)
			return
		}
		o.Errors = append(o.Errors, o.open)
	case "WARNING":
		if len(o.Warnings) == maxWarnings {
			o.WarningsTruncated++
			return
		}
		o.Warnings = append(o.Warnings, LogWarning{Time: at, DB: db, Logger: logger, Message: msg})
	}
}

func newTestFailure(kind, desc, paren, logger string) *TestFailure {
	f := &TestFailure{Kind: kind, Test: desc}
	if m := addonModule.FindStringSubmatch(logger); m != nil && strings.Contains(logger, ".tests") {
		f.Module = m[1]
	}
	if m := addonModule.FindStringSubmatch(paren); m != nil {
		if f.Module == "" {
			f.Module = m[1]
		}
		if !strings.Contains(desc, ".") {
			f.Test = paren[strings.LastIndexByte(paren, '.')+1:] + "." + desc
		}
	}
	return f
}

// continueEntry adds a traceback line to the open error or failure.
func (o *Outcome) continueEntry(line string) {
	if o.open == nil {
		return
	}
	o.open.Lines = append(o.open.Lines, line)
	if fr := traceFrame.FindStringSubmatch(line); fr != nil {
		n, _ := strconv.Atoi(fr[2])
		frame := Frame{File: fr[1], Line: n, Func: strings.TrimSpace(fr[3])}
		o.open.Frames = append(o.open.Frames, frame)
		o.open.Exception, o.excDone = "", false
		if o.failure != nil && o.failure.Module != "" && strings.Contains(frame.File, "/"+o.failure.Module+"/") {
			o.failure.File, o.failure.Line = frame.File, frame.Line
		}
		return
	}
	text := strings.TrimSpace(line)
	if text == "" || len(o.open.Frames) == 0 {
		return
	}
	switch {
	case !o.excDone && line[0] != ' ' && line[0] != '\t':
		o.open.Exception, o.excDone = text, true
	case o.excDone && strings.HasSuffix(o.open.Exception, ":") && len(o.open.Exception) < maxExceptionLen:
		// Odoo's UserError and ValidationError print their message on the
		// lines after `Type:`.
		o.open.Exception += " " + text
	default:
		return
	}
	if o.failure != nil {
		o.failure.Message = o.open.Exception
	}
}
