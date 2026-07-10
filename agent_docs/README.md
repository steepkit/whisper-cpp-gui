# agent_docs/ — エージェント向け詳細資料

共通ルールは [`../AGENTS.md`](../AGENTS.md)。ここには「毎回のプロンプトに入れると長すぎるが、特定タスクで必要になる」資料を置く。

## Codex プロンプトテンプレート(`codex/`)

Codex を呼ぶときは必ずここのテンプレートから組み立てる(規律と観点の抜け防止)。使い分けとゲート対応は [`../docs/agentic_workflow.md`](../docs/agentic_workflow.md) §8。

| ファイル | 用途 | sandbox |
|---|---|---|
| `codex/review_blind.md` | G2: 独立コードレビュー | read-only |
| `codex/review_adversarial.md` | G4: 攻撃者視点レビュー | read-only |
| `codex/design_alternative.md` | G0: 独立設計案 | read-only |
| `codex/implement_issue.md` | Issue 実装 | workspace-write |
| `codex/rescue.md` | 行き詰まりの引き取り再実装 | workspace-write |
| `codex/qa_exploratory.md` | リリース前の探索的 QA | workspace-write |
| `codex/docs_review.md` | 手順書・README 検証 | read-only |

## 詳細資料

- `stub_contract.md` — スタブの入出力契約
- `whisper_cli_reference.md` — brew 版 whisper-cli の実仕様(M0-3 の結果から作成)
- `sse_protocol.md` — SSE イベントの型定義(M1-5 完了時に作成)
