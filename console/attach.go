package console

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const (
	wsReadLimit  = 1 << 20
	wsPongWait   = 60 * time.Second
	wsPingPeriod = 30 * time.Second
	wsWriteWait  = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	},
}

type resizeMsg struct {
	Resize *struct {
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	} `json:"resize"`
}

func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	ls := s.mgr.Get(r.PathValue("id"))
	if ls == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	if c, err := strconv.Atoi(r.URL.Query().Get("cols")); err == nil {
		if rows, err := strconv.Atoi(r.URL.Query().Get("rows")); err == nil {
			ls.Resize(uint16(c), uint16(rows))
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	replay, ch, cancel := ls.Subscribe()
	defer cancel()

	conn.SetReadLimit(wsReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(wsPongWait))
	})

	go func() {
		defer conn.Close()
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		if len(replay) > 0 {
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.BinaryMessage, replay); err != nil {
				return
			}
		}
		for {
			select {
			case chunk, ok := <-ch:
				if !ok {
					return
				}
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
					return
				}
			case <-ticker.C:
				_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	for {
		kind, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch kind {
		case websocket.BinaryMessage:
			if err := ls.WriteInput(data, ch); err != nil {
				_ = conn.Close()
				return
			}
		case websocket.TextMessage:
			var msg resizeMsg
			if json.Unmarshal(data, &msg) == nil && msg.Resize != nil {
				ls.Resize(msg.Resize.Cols, msg.Resize.Rows)
			}
		}
	}
	_ = conn.Close()
}
