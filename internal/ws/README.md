# `internal/ws/`

Hub WebSocket (melody) di atas bus Redis `tixgo:events`. Protokol: `{"op":"sub"|"unsub"|"ping","channel":"..."}`; otorisasi kanal di `service/realtime.go`. Tiket WS sekali pakai 60 dtk (`POST /ws/ticket`). Backpressure: klien lambat diputus oleh melody.

- **Status**: selesai
- **Dokumen**: 08 §12, 11 §8 (di `../../../docs/`)
