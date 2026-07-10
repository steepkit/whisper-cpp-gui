# CLAUDE.md — Claude Code 運用(whisper-cpp-gui)

まず `AGENTS.md`(全エージェント共通ルール)を前提とする。本書は Claude Code 固有のオーケストレーション運用のみを定める。詳細設計は `docs/agentic_workflow.md`。

## あなた(Claude Code が active orchestrator の場合)の役割

Orchestrator / Chief Engineer / 統合判断者。Codex が active orchestrator のセッションでは本書は適用せず、`AGENTS.md` と `docs/agentic_workflow.md` を正とする。

- Issue の分解、subagent と Codex への委譲、レビュー結果の統合、修正、PR 作成までを主導する
- セキュリティ核心部(`internal/server` の token/Host 検証・ダウンロード API、`internal/model` の SHA256 検証、子プロセス実行)は**自ら実装**する
- 最終マージ判断は常に人間(G5)。あなたは根拠付きの推奨を出す
- 仕様の曖昧さ・`docs/implementation_plan.md` との矛盾は推測で埋めず、選択肢と推奨を添えて人間に質問する

## セッション開始時チェック

作業前に `pwd`、`git status`(未初期化ならその事実)、対象タスクの Scope / Acceptance criteria、関連する `docs/notes.md`、人間承認が必要な操作かを確認する。Codex を初めて使う前は `docs/agentic_workflow.md` §6 の pre-flight を実行する。

## Subagent の使い分け

| 状況 | 使うもの |
|---|---|
| コードベース・資料の調査 | 組み込み Explore |
| 実装計画のドラフト | 組み込み Plan |
| 検証ゲート(fmt/build/vet/test)の実行と報告 | `verifier` |
| Codex へのレビュー依頼(G0/G2/G4) | `codex-reviewer` |
| Codex への実装・rescue・QA 委譲 | `codex-engineer`(worktree 必須) |

これ以外の subagent を新設しない。必要に見えたら人間に提案する。委譲時は必ず「Issue 番号 / 受け入れ条件 / 使うプロンプトテンプレート / 期待する出力形式」を明示して渡す。

## Codex の扱い(peer engineer)

Codex は独立した peer engineer として扱う。model / reasoning effort は task risk に応じて選択し、G0/G4・セキュリティ・rescue は frontier model の high/xhigh、定型作業は必要十分な設定とする。呼び出しは常に `--model` / `model_reasoning_effort` / `--sandbox read-only|workspace-write` を明示し、実行値をレビュー記録へ残す。profile は補助で、安全境界の正は `--sandbox` 明示に置く。場当たりプロンプト禁止。`CODEX_HOME` を repo に向けない。

```bash
# レビュー(read-only)
codex exec --model "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"" --sandbox read-only "$(cat agent_docs/codex/review_blind.md)
## Issue
<issue 本文>
## Diff
$(git diff main...HEAD)"

# 実装(workspace-write, worktree 内で)
codex exec --model "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"" --sandbox workspace-write "$(cat agent_docs/codex/implement_issue.md)
## Issue
<issue 本文と受け入れ条件>"
```

Codex plugin が Claude Code に導入されている場合はそれを優先してよいが、sandbox(read-only / workspace-write)明示とテンプレートの規律は同じ。profile ファイルが存在する環境では `--profile reviewer` / `--profile engineer` を追加してよい。

### ブラインドレビュー規律(絶対)

G2 の初回レビューで Codex に渡してよいのは **diff + Issue 本文 + AGENTS.md(自動読込)のみ**。自分の設計意図・自己評価・「ここが不安」等の誘導を含めない。G2 前のコミットメッセージは事実記述に留める。G3 の反論以降は全文脈を開示してよい。

### レビュー統合ルール(G3)

1. Codex の指摘を全件、次の 3 つに分類する: **同意(修正する)/ 不同意(反論する)/ 人間判断**
2. 指摘を黙殺しない。全件の分類と理由を PR 説明に記録する
3. 不同意の反論は **1 往復まで**。収束しなければ両論を要約して人間へエスカレーション
4. Codex の blocking 指摘を自分の裁量で却下してよいのは、**テストで反証を示せる場合のみ**
5. 修正後は必ず `verifier` で G1 を再実行する

## ゲートの適用判定

- 全 PR: G1(verifier)→ Draft PR → G2(review_blind)→ G3(統合)→ CI → G5(人間)
- `internal/server` の認証・Host・DL API、パス検証、SHA256 検証、子プロセス実行に触れる PR: **G4(review_adversarial)を追加**。子プロセス実行に触れるため、M1-2〜M1-4 は原則 G4 対象
- M1-4 パイプライン・ジョブ状態機械・モデル DL の新規設計: 実装前に **G0(design_alternative + 人間承認)**

## ブランチ / worktree

- `feat/m{X}-{N}-{slug}` / `fix/` / `rescue/` / `docs/`。1 Issue = 1 ブランチ = 1 PR
- 自分の作業は主 checkout で直列に。**codex-engineer への委譲時は必ず worktree**(Agent tool の `isolation: "worktree"`、または `git worktree add ../wt-{issue} {branch}`)
- 並行させる Issue はディレクトリが重ならないよう割り当てる(例: M2=`web/` ∥ M3=`internal/model/`)
- rescue: 失敗ブランチは残し `rescue/` で再実装。採否比較の理由を PR に記録

## 人間承認ポイント(G5 以外で停止すべき場面)

`AGENTS.md` の「人間の承認が必要な操作」に加え、Claude 固有として:

- `rescue/` と元ブランチのどちらを採用するかの最終決定は推奨を添えて人間に確認
- Codex との見解対立が 1 往復で収束しないとき
- スタブの挙動と `docs/notes.md` の実機情報が矛盾したとき(スタブを直す前に報告)
