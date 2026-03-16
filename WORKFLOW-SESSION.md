# WORKFLOW-SESSION.md
# Session: NX-phase16-sse-streaming
# Date: 2026-03-17

## What changed — Nexus Phase 16 (ADR-015)

SSE streaming endpoint. GET /events/stream fans out platform events to
connected observer services in real-time. Polling endpoints unchanged.

## New files
- internal/sse/broker.go              — fan-out broker, slow-client eviction
- internal/api/handler/stream.go      — SSE handler, keepalive every 15s

## Modified files
- internal/state/events.go            — SSEPublisher interface, WithBroker()
                                        write() notifies broker after store write
- internal/api/server.go              — SSEBroker field in ServerConfig
                                        GET /events/stream registered when broker set

## Apply

cd ~/workspace/projects/apps/nexus && \
unzip -o /mnt/c/Users/harsh/Downloads/engx-drop/nexus-phase16-sse-streaming-20260317.zip -d . && \
go build ./...

## Wire broker in main.go (manual step — see instructions below)

After go build passes, add to cmd/engxd/main.go:
  1. Import: "github.com/Harshmaury/Nexus/internal/sse"
  2. Create broker: broker := sse.NewBroker()
  3. Pass to ServerConfig: SSEBroker: broker
  4. Pass to each EventWriter: events.WithBroker(broker)

## Verify

pkill engxd && go install ./cmd/engxd/ && cp ~/go/bin/engxd ~/bin/engxd && engxd &
sleep 2
curl -s --no-buffer -H "X-Service-Token: <token>" http://127.0.0.1:8080/events/stream
# Should see: : connected
# Then trigger an event — see it stream in real-time

## Commit

git add \
  internal/sse/broker.go \
  internal/api/handler/stream.go \
  internal/state/events.go \
  internal/api/server.go \
  WORKFLOW-SESSION.md && \
git commit -m "feat(phase16): SSE streaming GET /events/stream (ADR-015)" && \
git tag v1.2.0-phase16 && \
git push origin main --tags
