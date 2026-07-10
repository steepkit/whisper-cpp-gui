# SSE protocol

M1-5 で確定した job SSE の wire contract。M3 の model download SSE も同じ framing と再接続規則を使う。

## Authentication

- Endpoint: `GET /api/jobs/{id}/events?token={session-token}`
- Browser `EventSource` は header を設定できないため query token を使う。
- `X-Auth-Token` も query なしなら受理するが、両方指定・重複 query・不正 token は `401`。
- 通常 API と download API は header token のみ。download query token は禁止。

## Frames

接続直後の最初の frame は必ず authoritative snapshot:

```text
event: snapshot
data: {"id":"job-...","status":"running",...}

```

実行中の差分 frame:

```text
event: state|progress|log
data: {"type":"progress","job_id":"job-...","progress":42}

```

- `progress` は未判定時 `null`、判定済みなら `0`〜`100`。`null` と `0` を同一視しない。
- `logs`, `outputs`, `warnings` は snapshot では常に JSON array。クライアントは snapshot 受信時にローカル配列を置換する。
- raw internal error と内部 output path は送らない。UI は `error_code`, `stderr_tail`, `recovery_path` を使う。
- 15 秒ごとの `: keep-alive` comment は状態を変更しない。

## Completion and reconnect

- 終端時は薄い state event の代わりに最新 snapshot を送って stream を閉じる。
- subscriber overflow 時も可能なら最新 snapshot を送って stream を閉じる。
- `EventSource` 再接続後も最初の snapshot を正として状態と保持ログを置換する。
- `id:` field と replay buffer は提供しない。`Last-Event-ID` による差分再送は保証しない。
- client 切断時は直ちに unsubscribe し、購読枠を解放する。

## Downloads

Snapshot/outputs API が返す `outputs` は basename の allowlist。GUI は通常の `<a>` ではなく `X-Auth-Token` 付き `fetch` で `/api/download/{id}/{file}` を取得し、Blob URL から保存する。
