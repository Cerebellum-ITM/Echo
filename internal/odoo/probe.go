package odoo

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

//go:embed probe.py
var probeTemplate string

const probeArgsPlaceholder = "__ECHO_PROBE_ARGS__"

// ProbeFramePrefix starts every stdout line that carries a chunk of the probe
// result: `@@ECHO_PROBE@@ <seq> <total> <base64 chunk>`.
const ProbeFramePrefix = "@@ECHO_PROBE@@"

// ProbeExpr is one `-c` expression; Name is empty for an unnamed one.
type ProbeExpr struct {
	Name string `json:"name"`
	Expr string `json:"expr"`
}

// ProbeRequest is what the probe template evaluates. Kind is one of eval,
// script, ref, read, fields, param or arch; the other fields apply per kind.
type ProbeRequest struct {
	Kind     string          `json:"kind"`
	Exprs    []ProbeExpr     `json:"exprs"`
	Script   string          `json:"script"`
	Model    string          `json:"model"`
	XMLID    string          `json:"xmlid"`
	Domain   json.RawMessage `json:"domain"`
	Fields   []string        `json:"fields"`
	Names    []string        `json:"names"`
	Limit    int             `json:"limit"`
	Order    string          `json:"order"`
	Count    bool            `json:"count"`
	Key      string          `json:"key"`
	ViewType string          `json:"view_type"`
	Reveal   bool            `json:"reveal"`
	Major    int             `json:"major"`
}

// ProbeScript renders the Python fed to ProbeShell. The request travels as
// base64 of its JSON, so no user text ever becomes Python source.
func ProbeScript(req ProbeRequest) ([]byte, error) {
	args, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encode probe request: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(args)
	return []byte(strings.Replace(probeTemplate, probeArgsPlaceholder, encoded, 1)), nil
}

// ProbeShell is Shell with Odoo's logging cut to warnings, so the boot adds a
// few lines around the result instead of hundreds.
func ProbeShell(c Conn) Cmd {
	return WithLogLevel(Shell(c), "warn")
}

var (
	errNoProbeFrame   = errors.New("no result frame in the output")
	errMalformedFrame = errors.New("malformed result frame")
)

// DecodeProbeFrames reassembles the result frames found among lines (other
// lines are ignored) and returns the JSON payload. Chunks must arrive in
// order 1..total; a missing, repeated or malformed chunk is an error.
func DecodeProbeFrames(lines []string) (json.RawMessage, error) {
	var data strings.Builder
	next, total := 1, 0
	for _, line := range lines {
		parts := strings.Fields(line)
		if len(parts) == 0 || parts[0] != ProbeFramePrefix {
			continue
		}
		if len(parts) != 4 {
			return nil, errMalformedFrame
		}
		seq, seqErr := strconv.Atoi(parts[1])
		n, nErr := strconv.Atoi(parts[2])
		if seqErr != nil || nErr != nil || n < 1 {
			return nil, errMalformedFrame
		}
		if seq != next || (total != 0 && n != total) {
			return nil, fmt.Errorf("result frame %d/%d out of order (expected %d)", seq, n, next)
		}
		total = n
		next++
		data.WriteString(parts[3])
	}
	if total == 0 {
		return nil, errNoProbeFrame
	}
	if next <= total {
		return nil, fmt.Errorf("result incomplete: %d of %d frames", next-1, total)
	}
	payload, err := base64.StdEncoding.DecodeString(data.String())
	if err != nil {
		return nil, fmt.Errorf("decode result: %w", err)
	}
	if !json.Valid(payload) {
		return nil, errors.New("decode result: not JSON")
	}
	return payload, nil
}
