package odoo

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

func TestProbeScriptEmbedsArgsOnlyAsBase64(t *testing.T) {
	marker := `PROBE_MARKER"); import os; os.system("x`
	req := ProbeRequest{
		Kind:   "eval",
		Exprs:  []ProbeExpr{{Name: "n", Expr: marker}},
		Domain: json.RawMessage(`[["name","=","` + "PROBE_DOMAIN" + `"]]`),
	}
	script, err := ProbeScript(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"PROBE_MARKER", "PROBE_DOMAIN", probeArgsPlaceholder} {
		if bytes.Contains(script, []byte(raw)) {
			t.Errorf("rendered script contains %q", raw)
		}
	}
	args, _ := json.Marshal(req)
	if !bytes.Contains(script, []byte(`b64decode("`+base64.StdEncoding.EncodeToString(args)+`")`)) {
		t.Error("rendered script does not carry the request as base64")
	}
}

func TestProbeShellLowersLogLevel(t *testing.T) {
	got := strings.Join(ProbeShell(Conn{DB: "mydb"}), " ")
	if want := "odoo shell -d mydb --no-http --log-level=warn"; got != want {
		t.Fatalf("ProbeShell argv\n got: %q\nwant: %q", got, want)
	}
}

// probeFrames encodes payload the way the template does, in chunks of size.
func probeFrames(payload string, size int) []string {
	data := base64.StdEncoding.EncodeToString([]byte(payload))
	var chunks []string
	for len(data) > size {
		chunks = append(chunks, data[:size])
		data = data[size:]
	}
	chunks = append(chunks, data)
	frames := make([]string, len(chunks))
	for i, c := range chunks {
		frames[i] = fmt.Sprintf("%s %d %d %s", ProbeFramePrefix, i+1, len(chunks), c)
	}
	return frames
}

func TestDecodeProbeFramesRoundTrip(t *testing.T) {
	payload := `{"ok": true, "kind": "eval", "result": {"a": [1, 2, 3]}, "stdout": "hi\n"}`
	frames := probeFrames(payload, 8)
	if len(frames) < 3 {
		t.Fatalf("want several chunks, got %d", len(frames))
	}
	lines := []string{"2026-10-19 10:00:00,000 1 WARNING db odoo.modules: noise"}
	for _, f := range frames {
		lines = append(lines, f+"\r", "")
	}
	got, err := DecodeProbeFrames(lines)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("payload\n got: %s\nwant: %s", got, payload)
	}
}

func TestDecodeProbeFramesRejectsOutOfOrder(t *testing.T) {
	frames := probeFrames(`{"ok": true, "result": "abcdefghijklmnop"}`, 8)
	frames[0], frames[1] = frames[1], frames[0]
	if _, err := DecodeProbeFrames(frames); err == nil || !strings.Contains(err.Error(), "out of order") {
		t.Fatalf("want out-of-order error, got %v", err)
	}
}

func TestDecodeProbeFramesMissingChunk(t *testing.T) {
	frames := probeFrames(`{"ok": true, "result": "abcdefghijklmnop"}`, 8)
	if _, err := DecodeProbeFrames(frames[:len(frames)-1]); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("want incomplete error, got %v", err)
	}
	if _, err := DecodeProbeFrames([]string{"only a log line"}); err != errNoProbeFrame {
		t.Fatalf("want errNoProbeFrame, got %v", err)
	}
}

func TestProbeTemplateCompiles(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	script, err := ProbeScript(ProbeRequest{Kind: "eval", Exprs: []ProbeExpr{{Expr: "1"}}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), 'probe.py', 'exec')")
	cmd.Stdin = bytes.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("probe template does not compile: %v\n%s", err, out)
	}
}
