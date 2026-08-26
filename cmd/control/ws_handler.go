package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nats-io/nats.go"

	"github.com/Ritesh956/SenseGrid/internal/tracing"
)

const (
	wsWriteWait  = 10 * time.Second
	wsPongWait   = 60 * time.Second
	wsPingPeriod = (wsPongWait * 9) / 10 // stays under wsPongWait
	wsSendBuffer = 64
)

// wsRelaySubjects are the NATS wildcard subjects a console viewer fans out
// over its socket — metrics.> (windowed MetricEvent, the purpose-built
// chart signal, not raw telemetry.>), alerts.> and rollout.>. Every one of
// these is already published by an existing service (cmd/processor,
// internal/rollout); the WS handler only ever subscribes, it never
// publishes. Device-shadow desired/reported state deliberately isn't
// here — it's MQTT retained-publish only (internal/shadow), not NATS, so
// the console polls GET /v1/devices/{id}/shadow for that instead of this
// endpoint gaining new publish-side plumbing.
var wsRelaySubjects = []struct{ kind, subject string }{
	{"metric", "metrics.>"},
	{"alert", "alerts.>"},
	{"rollout", "rollout.>"},
}

// wsFrame is the envelope every relayed NATS message is wrapped in before
// being written to the socket.
type wsFrame struct {
	Type    string          `json:"type"`
	Subject string          `json:"subject"`
	Payload json.RawMessage `json:"payload"`
	TS      int64           `json:"ts"`
}

// traceMetricRelay adds one more hop to a reading's trace: internal/window's
// MetricEvent carries the trace_id of the reading it was computed from
// (AlertEvent/RolloutEvent don't — confirmed, no bridging attempted for
// those two frame kinds), so a metrics.> message reaching a console viewer
// is the last server-side point before the browser. Phases before this one
// already got the trace to processor.persist/processor.window
// (cmd/processor); this is what makes it "browsable end-to-end ... device
// → console" per the Phase 6 DoD, short of adding a browser OTel SDK, which
// would be scope creep for this phase.
func traceMetricRelay(payload []byte) {
	var evt struct {
		TraceID string `json:"trace_id"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.TraceID == "" {
		return
	}
	_, span := tracing.Tracer("sensegrid/control").Start(
		tracing.ContextWithReadingTrace(context.Background(), evt.TraceID), "control.ws_relay")
	span.End()
}

// registerWSHandler wires GET /v1/ws — the Phase 5 console's live feed.
// Auth is a ?token= query param, not an Authorization header: a browser's
// WebSocket API can't set custom headers on the handshake request, so this
// reuses verifyToken (the same claims check requireRole's header path
// uses) directly instead of going through verifyBearer.
func registerWSHandler(mux *http.ServeMux, logger *slog.Logger, nc *nats.Conn, m *metrics, issuer string, signingKey []byte) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		// web/console is a separate origin from cmd/control (a different
		// port at minimum); gorilla's default same-origin check would
		// reject that handshake. Auth here is the token below, not Origin,
		// so this is intentionally permissive rather than hardcoding or
		// tracking every console deployment's origin.
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux.HandleFunc("GET /v1/ws", func(w http.ResponseWriter, r *http.Request) {
		role, _, err := verifyToken(r.URL.Query().Get("token"), issuer, signingKey)
		if err != nil || !roleAllows(role, roleViewer) {
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing token")
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Warn("ws: upgrade failed", "err", err)
			return
		}
		serveWSConn(conn, nc, m, logger)
	})
}

// serveWSConn owns one console viewer's connection for its lifetime: its
// own NATS subscriptions (fine at this project's scale — Phase 7 is where
// WS fan-out under many concurrent viewers gets load-tested, per the
// blueprint's own risk register, not assumed correct here), a single
// writer goroutine (gorilla requires one goroutine per connection doing
// writes), and ping/pong keepalive so a dead peer — in either direction —
// is noticed within wsPongWait, not "eventually".
func serveWSConn(conn *websocket.Conn, nc *nats.Conn, m *metrics, logger *slog.Logger) {
	defer conn.Close()

	m.wsClientsConnected.Inc()
	defer m.wsClientsConnected.Dec()

	send := make(chan []byte, wsSendBuffer)
	closed := make(chan struct{})
	var closeOnce sync.Once
	stop := func() { closeOnce.Do(func() { close(closed) }) }

	relay := func(kind string) nats.MsgHandler {
		return func(msg *nats.Msg) {
			if kind == "metric" {
				traceMetricRelay(msg.Data)
			}
			frame, err := json.Marshal(wsFrame{
				Type: kind, Subject: msg.Subject, Payload: msg.Data, TS: time.Now().UnixMilli(),
			})
			if err != nil {
				return
			}
			select {
			case send <- frame:
			default:
				// Slow client: drop rather than block NATS delivery to
				// every other subscriber this process has.
			}
		}
	}

	subs := make([]*nats.Subscription, 0, len(wsRelaySubjects))
	for _, s := range wsRelaySubjects {
		sub, err := nc.Subscribe(s.subject, relay(s.kind))
		if err != nil {
			logger.Error("ws: nats subscribe failed", "err", err, "subject", s.subject)
			continue
		}
		subs = append(subs, sub)
	}
	defer func() {
		for _, sub := range subs {
			_ = sub.Unsubscribe()
		}
	}()

	// The console never sends data frames; this goroutine exists solely to
	// process pong control frames and notice a client-initiated close or a
	// dead connection (ReadMessage returning an error either way).
	go func() {
		defer stop()
		_ = conn.SetReadDeadline(time.Now().Add(wsPongWait))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(wsPongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(wsPingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-closed:
			return
		case frame := <-send:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(wsWriteWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
