// hukukfix-host is the native messaging host the browser extension talks to.
//
// It is the only door between the browser and the app: the extension sends a
// request ("call"), the host forwards it to the app's loopback API and returns
// the answer, starting the app first when asked to. The browser itself never
// opens a connection to 127.0.0.1, so browser restrictions on local network
// access cannot break the integration, and the API's access token never
// leaves this machine's app/host pair.
//
// Protocol (Chrome native messaging): each message is a 32-bit little-endian
// length followed by UTF-8 JSON, on stdin/stdout.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"hukukfixdm/internal/shared"
)

type request struct {
	Action string          `json:"action"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
	Launch bool            `json:"launch"` // start the app if it is not running
}

type response struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Version string          `json:"version,omitempty"`
	Status  int             `json:"status,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var errNotRunning = errors.New("not_running")

func readMessage(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n == 0 || n > 4<<20 {
		return nil, fmt.Errorf("bad message length %d", n)
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func writeMessage(w io.Writer, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	out := make([]byte, 4, 4+len(body))
	binary.LittleEndian.PutUint32(out, uint32(len(body)))
	_, _ = w.Write(append(out, body...))
}

// do sends one request to the app. HTTP/1.0 keeps the exchange trivial: the
// server answers and closes, no chunked encoding, no HTTP client linked in.
func do(b shared.BridgeInfo, method, path string, body []byte) (int, []byte, error) {
	if b.Port == 0 || b.Token == "" {
		return 0, nil, errNotRunning
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(b.Port), 900*time.Millisecond)
	if err != nil {
		return 0, nil, errNotRunning
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	head := fmt.Sprintf("%s %s HTTP/1.0\r\nHost: 127.0.0.1:%d\r\nX-HFDM-Token: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n",
		method, path, b.Port, b.Token, len(body))
	if _, err := conn.Write(append([]byte(head), body...)); err != nil {
		return 0, nil, err
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return 0, nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return 0, nil, errors.New("bad response")
	}
	status, _ := strconv.Atoi(parts[1])
	for { // skip the headers
		h, err := br.ReadString('\n')
		if err != nil {
			return status, nil, err
		}
		if strings.TrimSpace(h) == "" {
			break
		}
	}
	data, err := io.ReadAll(io.LimitReader(br, 900<<10)) // native messages are capped at 1 MB
	return status, data, err
}

func alive() bool {
	b, err := shared.ReadBridgeInfo()
	if err != nil {
		return false
	}
	status, _, err := do(b, "GET", "/v1/status", nil)
	return err == nil && status == 200
}

const (
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// launch starts the app in the tray, detached from the browser so it keeps
// running when the browser (and this host) exit.
func launch() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	app := filepath.Join(filepath.Dir(self), shared.AppExe)
	if _, err := os.Stat(app); err != nil {
		return fmt.Errorf("%s not found next to the host", shared.AppExe)
	}
	start := func(flags uint32) error {
		cmd := exec.Command(app, "--background")
		cmd.Dir = filepath.Dir(app)
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	// Browsers may run the host inside a job object; leave it when allowed.
	if err := start(detachedProcess | createNewProcessGroup | createBreakawayFromJob); err != nil {
		return start(detachedProcess | createNewProcessGroup)
	}
	return nil
}

// ensure makes sure the app answers, starting it when allowed to.
func ensure(start bool) error {
	if alive() {
		return nil
	}
	if !start {
		return errNotRunning
	}
	if err := launch(); err != nil {
		return err
	}
	// The very first start creates the WebView profile and can take a while.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
		if alive() {
			return nil
		}
	}
	return errors.New("the app did not start in time")
}

func validPath(p string) bool {
	return strings.HasPrefix(p, "/v1/") && len(p) < 512 && !strings.ContainsAny(p, " \r\n\t")
}

func call(req request) response {
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	if (method != "GET" && method != "POST") || !validPath(req.Path) {
		return response{Error: "bad_request"}
	}
	var body []byte
	if method == "POST" && len(req.Body) > 0 && string(req.Body) != "null" {
		body = req.Body
	}
	for attempt := 0; attempt < 2; attempt++ {
		b, _ := shared.ReadBridgeInfo()
		status, data, err := do(b, method, req.Path, body)
		if err == nil && status != 401 {
			if !json.Valid(data) {
				data, _ = json.Marshal(strings.TrimSpace(string(data)))
			}
			return response{OK: status >= 200 && status < 300, Status: status, Data: data, Error: errorFor(status)}
		}
		// Not running (or a stale bridge file): start it once if allowed.
		if attempt == 0 {
			if e := ensure(req.Launch); e != nil {
				return response{Error: e.Error()}
			}
		}
	}
	return response{Error: errNotRunning.Error()}
}

func errorFor(status int) string {
	if status >= 200 && status < 300 {
		return ""
	}
	return "http_" + strconv.Itoa(status)
}

// logLine keeps a small trail for troubleshooting: which browser called, what
// it asked for and how it went. Never the request bodies (URLs, cookies).
func logLine(format string, args ...any) {
	path := filepath.Join(shared.DataDir(), "host.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 256<<10 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05.000"), fmt.Sprintf(format, args...))
}

func main() {
	caller := "-"
	if len(os.Args) > 1 {
		caller = os.Args[1] // the browser passes the calling extension's origin
	}
	// sendNativeMessage sends one message and closes stdin; serve until EOF.
	for {
		msg, err := readMessage(os.Stdin)
		if err != nil {
			if err != io.EOF {
				logLine("%s read error: %v", caller, err)
			}
			return
		}
		var req request
		if err := json.Unmarshal(msg, &req); err != nil {
			writeMessage(os.Stdout, response{Error: "invalid message"})
			logLine("%s invalid message", caller)
			continue
		}
		start := time.Now()
		var res response
		switch req.Action {
		case "call":
			res = call(req)
		case "connect": // make sure the app is running
			if err := ensure(true); err != nil {
				res = response{Error: err.Error()}
			} else {
				res = response{OK: true}
			}
		case "ping":
			res = response{OK: true}
		default:
			res = response{Error: "unknown action"}
		}
		res.Version = shared.Version
		writeMessage(os.Stdout, res)
		outcome := "ok"
		if !res.OK {
			outcome = "error=" + res.Error
		}
		logLine("%s %s %s %s -> %s (%d ms)", caller, req.Action, req.Method, req.Path, outcome, time.Since(start).Milliseconds())
	}
}
