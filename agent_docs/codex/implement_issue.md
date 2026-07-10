# Issue 実装依頼

あなたはこのリポジトリのシニア Go エンジニアです。以下の Issue を、受け入れ条件を満たすまで実装してください。作業ディレクトリはあなた専用の worktree です。この外には書き込まないでください。

## 必読

- AGENTS.md(規約の正)。特に: **Go 標準ライブラリのみ / スタブ駆動テスト / セキュリティ要件 / OS 依存の局所化 / UI 文字列の i18n 外部化 / レイヤ規約**
- 関連仕様は `docs/implementation_plan.md` の該当マイルストーン

## 完了の定義(すべて満たすこと)

1. Issue の受け入れ条件をすべて満たす
2. `gofmt_out="$(gofmt -l .)" && test -z "$gofmt_out"`、`go build ./...` / `go vet ./...` / `go test ./...` が通る
3. 受け入れ条件に対応するテストを追加(`httptest` ハンドラテスト + スタブ E2E、テーブル駆動推奨)
4. `go.mod` に依存を追加していない。禁止技術を使っていない
5. UI 変更時は `web/locales/ja.json` にキー定義し、ハードコードしない

## やってはいけないこと

- 無関係なリファクタリングの混入(1 Issue = 1 変更単位)
- 実機でしか確認できない項目をコード内 TODO で誤魔化す → 代わりに、その項目を報告に列挙する(依頼側が `docs/notes.md` に移す)
- 仕様の曖昧さを勝手に解釈して進める → 判断が必要な点は報告の「Questions」に挙げ、妥当なデフォルトで進めた場合はそれを明記する

## 出力形式(実装後の報告)

```
## Summary
<何を実装したか>

## Files changed
<git diff --stat 相当>

## Acceptance criteria
- <条件> : done (<テスト名 / 実装箇所>)

## Verification
gofmt / build / vet / test の結果

## Needs real-machine check
- <実機でしか確認できない項目>

## Questions / assumptions
- <人間/依頼側に確認したい点、置いた仮定>
```

---
(以下、依頼側が Issue 本文と受け入れ条件を結合する)
