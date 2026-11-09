package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/coder/websocket"
	"github.com/creack/pty"

	"github.com/bogdanlinitskiy/claude-inbox/internal/state"
)

const (
	initialCols = 120
	initialRows = 40
	maxSide     = 1000
	// readLimit lets a large paste through in one message.
	readLimit     = 1 << 20
	outputChunk   = 32 << 10
	controlResize = "resize"
	terminalType  = "xterm-256color"
)

// control is a text message from xterm.js; keystrokes come as binary messages.
type control struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// terminal attaches a tmux client to the session inside a PTY and connects it to the page's xterm.js. Closing the
// socket ends only that client: the session keeps running.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	info, err := s.cfg.Sessions.Get(r.PathValue("id"))
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	if info.State.Kind == state.KindEnded {
		writeError(w, http.StatusConflict, codeConflict, "This session has ended.")
		return
	}
	args, err := s.cfg.Sessions.Attach(info.ID)
	if err != nil {
		s.sessionError(w, r, err)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		s.cfg.Logger.Error("accept terminal socket", "session", info.ID, "err", err)
		return
	}
	defer conn.CloseNow() //nolint:errcheck // a no-op after the normal Close below
	conn.SetReadLimit(readLimit)

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = terminalEnv()
	// pty.Start makes the PTY the attach client's controlling terminal, without which tmux never sees resizes
	// (docs/architecture.md, "Terminal"). The page's size comes with the request: a resize sent while the tmux
	// client is still starting can arrive before it handles SIGWINCH and be lost, leaving tmux drawing at the wrong
	// size.
	cols, rows := initialSize(r)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		s.cfg.Logger.Error("attach to tmux session", "session", info.ID, "err", err)
		if closeErr := conn.Close(websocket.StatusInternalError, "Could not attach to the session."); closeErr != nil {
			s.cfg.Logger.Error("close terminal socket", "session", info.ID, "err", closeErr)
		}
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		defer cancel()
		pumpOutput(ctx, conn, ptmx)
	}()
	if err := s.pumpInput(ctx, conn, ptmx); err != nil {
		s.cfg.Logger.Error("terminal input", "session", info.ID, "err", err)
	}
	cancel()
	s.detach(info.ID, cmd, ptmx)
	<-outputDone
	// When the page went away without a close frame (tab closed, reload) this fails, which is expected.
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		s.cfg.Logger.Debug("close terminal socket", "session", info.ID, "err", err)
	}
}

func pumpOutput(ctx context.Context, conn *websocket.Conn, ptmx *os.File) {
	buf := make([]byte, outputChunk)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			if writeErr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); writeErr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpInput forwards keystrokes and resizes until the page is gone. A failed read always means the socket is done
// (closed, reset by a closing tab, or cancelled), which is the normal end, not an error.
func (s *Server) pumpInput(ctx context.Context, conn *websocket.Conn, ptmx *os.File) error {
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return nil
		}
		if typ == websocket.MessageBinary {
			if _, err := ptmx.Write(data); err != nil {
				return err
			}
			continue
		}
		var c control
		if json.Unmarshal(data, &c) != nil || c.Type != controlResize {
			continue
		}
		cols, colsOK := side(c.Cols)
		rows, rowsOK := side(c.Rows)
		if !colsOK || !rowsOK {
			continue
		}
		if err := pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows}); err != nil {
			return err
		}
	}
}

// detach ends the tmux client; the session and its other clients are unaffected.
func (s *Server) detach(id string, cmd *exec.Cmd, ptmx *os.File) {
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		s.cfg.Logger.Error("stop tmux client", "session", id, "err", err)
	}
	var exitErr *exec.ExitError
	if err := cmd.Wait(); err != nil && !errors.As(err, &exitErr) {
		s.cfg.Logger.Error("wait for tmux client", "session", id, "err", err)
	}
	if err := ptmx.Close(); err != nil {
		s.cfg.Logger.Error("close terminal", "session", id, "err", err)
	}
}

// initialSize reads ?cols=&rows= from the socket URL, falling back to a default for missing or invalid values.
func initialSize(r *http.Request) (width, height uint16) {
	cols, colsErr := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, rowsErr := strconv.Atoi(r.URL.Query().Get("rows"))
	width, widthOK := side(cols)
	height, heightOK := side(rows)
	if colsErr != nil || rowsErr != nil || !widthOK || !heightOK {
		return initialCols, initialRows
	}
	return width, height
}

// side reports whether n is a terminal dimension the page may ask for, and returns it as the PTY wants it. The
// upper bound is what makes the conversion exact.
func side(n int) (uint16, bool) {
	if n <= 0 || n > maxSide {
		return 0, false
	}
	return uint16(n), true
}

// terminalEnv drops the variables of any tmux the server itself runs in, so the client attaches instead of
// refusing to nest.
func terminalEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") || strings.HasPrefix(kv, "TERM=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM="+terminalType)
}
