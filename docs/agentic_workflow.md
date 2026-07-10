# agentic_workflow.md — エージェント運用設計

whisper-cpp-gui の開発における AI エージェントの役割分担・レビューゲート・ブランチ戦略を定義する。共通規範は [`AGENTS.md`](../AGENTS.md)、Claude Code での具体的な運用手順は [`CLAUDE.md`](../CLAUDE.md) を参照。

## 1. 役割モデル

```
人間(Product Owner / 最終承認)
 └─ Active Orchestrator (Claude Code または Codex) — Chief Engineer / 統合判断
     ├─ Explore / Plan(組み込み)     … 調査・設計ドラフト
     ├─ verifier                      … 検証ゲート実行(build/vet/test/fmt)
     ├─ codex-reviewer ─→ Codex CLI   … read-only。ブラインド/対立的/設計レビュー
     └─ codex-engineer ─→ Codex CLI   … workspace-write。実装/rescue/QA/docs
```

- **Active Orchestrator** は問題分解、委譲、結果統合、最終判断を担う。自らも実装する(特にセキュリティ核心部)。2026-07-10 以降は Codex が引き継ぎ、Claude Code が復帰しても同じ gate と記録規律を使う
- **Independent peer reviewer** は orchestrator と別 context で実行し、初回レビューでは orchestrator の結論を意図的に隠す。model/effort は task risk に応じて選び、実行記録へ残す
- **subagent は上記で固定**。新しい subagent が必要に見えたら、まず既存の枠で表現できないか検討し、人間に提案する

### 役割ごとの定義(role / input / output / permissions / completion criteria)

| Agent | Role | Input | Output | Permissions | Completion criteria |
|---|---|---|---|---|---|
| Orchestrator | 統括・実装・統合・最終判断 | Issue, `docs/implementation_plan.md`, レビュー結果 | 実装, PR, 統合判断の記録 | フル(人間承認事項を除く) | 検証コマンド全通過 + G2 以降のゲート通過 |
| Explore | コード・資料調査 | 調査クエリ | 結論と根拠(該当箇所) | read-only | 質問に根拠付きで回答 |
| Plan | 実装計画ドラフト | Issue + 制約 | ステップ計画, 変更対象ファイル, トレードオフ | read-only | Orchestrator が計画を承認 or 差し戻し |
| verifier | 検証ゲート実行 | ブランチ/worktree パス | PASS/FAIL + 失敗ログ抜粋 | Bash(検証コマンドのみ)+ Read | 4 コマンドの結果を証拠付きで報告 |
| codex-reviewer | Codex へのレビュー委譲 | diff + Issue 本文 + プロンプトテンプレート | 構造化された指摘リスト(severity 付き) | Bash(`codex exec` read-only sandbox)+ Read | 指摘 0 件でも「確認した観点」を報告 |
| codex-engineer | Codex への実装委譲 | Issue + 受け入れ条件 + worktree | ブランチ上の実装 + 自己検証結果 | Bash(`codex exec` workspace-write, worktree 内限定) | 受け入れ条件充足 + 検証コマンド通過 |

## 2. タスク割り当て原則

| タスク種別 | 担当 | 理由 |
|---|---|---|
| セキュリティ核心部(M1-1, M1-5, M3-1 検証) | Orchestrator 実装/統合 + independent adversarial review | 最重要領域は統括者が直接統合し、独立した攻撃視点を当てる |
| 通常の Issue 実装(M0-1/2, M2-x, M3-2 等) | M0〜M1 は原則 Orchestrator、M2 以降は Orchestrator または engineer subagent(並行時) | 決定論的ハーネスと CI が固まる前に並列実装を広げない。M2 ∥ M3 は worktree で並行可 |
| 事前設計の妥当性確認(M1-4 パイプライン等) | Plan + Codex 代替設計(design_alternative) | 実装前に独立した代替案と比較 |
| 全 PR レビュー | codex-reviewer(ブラインド) | 独立判断の確保 |
| Orchestrator の実装が行き詰まった場合 | engineer subagent(rescue) | 同じ前提に囚われない再実装 |
| リリース前 QA | codex-engineer(qa_exploratory) | スタブ環境での探索的テスト |
| 手順書・README | Orchestrator 執筆 + independent docs review | 「手順書だけで導入できるか」の第三者視点 |

## 3. Review gates / Verification gates

すべての PR は G1 → Draft PR → G2 → G3 を通る。G0 / G4 は対象条件に該当する場合のみ。G5 は常に人間。Codex レビューは peer review であり、主判定はテスト・受け入れ条件・CI・人間承認に置く。

| Gate | 名称 | 実施者 | 内容 | 通過条件 |
|---|---|---|---|---|
| **G0** | 設計レビュー | Independent peer(design_alternative)+ 人間 | 対象: M1-4 パイプライン、ジョブ状態機械、モデル DL 設計。orchestrator 案を見せずに peer に同じ課題の設計をさせ、差分を比較 | 人間が設計を承認 |
| **G1** | 自己検証 | verifier | `gofmt_out="$(gofmt -l .)" && test -z "$gofmt_out"` / `go build` / `go vet` / `go test ./...`(スタブ E2E 含む) | 4 つ全通過。FAIL 時は実装者へ差し戻し |
| **Draft PR** | 記録面の確保 | Orchestrator | G1 通過後に draft PR を作る。GitHub 未初期化の bootstrap 期は `docs/reviews/bootstrap/` の一時ログで代替 | PR または代替ログがあり、G2/G3 の記録先が決まっている |
| **G2** | ブラインドレビュー | codex-reviewer(review_blind) | draft PR の diff + Issue 本文 + AGENTS.md のみを渡す。**Claude の設計意図・自己評価は渡さない** | Codex が blocking 指摘なし、または G3 で解消 |
| **G3** | 統合・修正 | Orchestrator | 指摘を「同意(修正)/不同意(反論 1 往復)/人間判断」に分類。修正後 G1 を再実行。**指摘は黙殺せず全件の扱いを PR に記録** | 全指摘がクローズ、G1 再通過 |
| **G4** | 対立的レビュー | codex-reviewer(review_adversarial) | 対象: `internal/server` の認証/Host/DL API、パス検証、SHA256 検証、子プロセス実行。子プロセス実行に触れるため、M1-2〜M1-4 は原則対象。攻撃者視点で具体的な攻撃手順を構成させる | 有効な攻撃経路なし、または修正済み |
| **G5** | 人間承認 | 人間 | implementation_plan.md §19 の承認ポイント + 全 PR の最終マージ | 人間が承認 |
| **CI** | 自動ゲート | GitHub Actions | ubuntu: vet+test、macos-14: ビルド+スモーク、tap: install+test | 全ワークフローがグリーン |

エスカレーション規則: G3 で orchestrator と peer reviewer の見解が 1 往復で収束しない場合、両論を要約して人間に判断を求める。**blocking 指摘を orchestrator が却下できるのは、テストで反証を示せる場合のみ。**

## 4. ブラインドレビュー規律(重要)

Codex の初回レビュー(G2)の独立性を守るため:

1. 渡すもの: draft PR の diff(bootstrap 期は `git diff`)、Issue 本文(受け入れ条件含む)、AGENTS.md(Codex は自動で読む)
2. 渡さないもの: orchestrator の実装方針メモ、自己レビュー結果、「ここが不安」等の誘導、コミットメッセージ以上の説明
3. コミットメッセージ自体も G2 前は事実記述に留める(設計弁明を書かない)
4. G3 以降(反論・再レビュー)では文脈を全開示してよい

## 5. Worktree / branch strategy

```
main                    # 保護。人間承認済みマージのみ
feat/m{X}-{N}-{slug}    # 例: feat/m1-3-job-queue。1 Issue = 1 ブランチ = 1 PR
fix/{slug}              # バグ修正
rescue/m{X}-{N}-{slug}  # Codex による rescue。元ブランチから分岐
docs/{slug}             # ドキュメントのみ
```

- Orchestrator は主 checkout で直列に作業する。**並行実装(M2 ∥ M3)時のみ** worktree を使う: engineer subagent への委譲は `git worktree add ../wt-m2-1 feat/m2-1-i18n` した隔離ツリーで行う
- worktree 内の Codex は workspace-write sandbox で当該ツリー外に書けない
- rescue 時: 失敗ブランチはそのまま残し、`rescue/` ブランチで別 agent がやり直す。採否は orchestrator が両者を比較して判断し、判断理由を PR に記録
- コンフリクト最小化のため、並行する Issue はディレクトリ単位で重ならないよう Orchestrator が割り当てる(M2=`web/`, M3=`internal/model/`)
- git 初期化・private GitHub リポジトリ作成は 2026-07-10 に人間承認済みであり、orchestrator が実行できる。public 化、main 保護緩和、release は引き続き人間承認事項

## 6. Codex CLI 運用と .codex/config.toml 方針

### 方針

- model / reasoning effort は task risk に比例させる。G0/G4、セキュリティ、rescue は利用可能な frontier model の high/xhigh、定型実装・探索・検証は必要十分な設定を選ぶ
- 非対話実行(`codex exec`)のみ。対話セッションは使わない(再現性とログのため)
- 呼び出し時は `--model` と `-c 'model_reasoning_effort=...'` を明示し、実行ログに値を残す。profile を使う場合も呼び出し側の明示値を優先する
- サンドボックスは**CLI フラグで明示**し、プロンプト側で権限を語らない:
  - レビュー/設計: `--sandbox read-only`
  - 実装/rescue/QA: `--sandbox workspace-write`
- profile は補助として使う。`$CODEX_HOME/reviewer.config.toml` / `engineer.config.toml` が未作成なら `--profile` を省略し、`--model` / `-c` / `--sandbox` 明示だけで実行する。安全境界の正は profile 解決ではなく、呼び出し時の `--sandbox` 明示に置く
- `approval_policy = "never"`(非対話のため。危険操作はサンドボックスで物理的に遮断する設計)
- AGENTS.md が Codex のプロジェクト指示書として自動で読まれることを前提に、AGENTS.md を「全エージェント共通の正」として維持する
- repo 内 `.codex/config.toml` は trusted project のときだけ読む。`CODEX_HOME` を repo に向ける運用は禁止(auth.json / logs / sessions も移動してしまうため)

### config.toml / profile 案

```toml
# ~/.codex/config.toml
approval_policy = "never"
sandbox_mode = "read-only"        # 既定は安全側
```

```toml
# ~/.codex/reviewer.config.toml
approval_policy = "never"
sandbox_mode = "read-only"
```

```toml
# ~/.codex/engineer.config.toml
approval_policy = "never"
sandbox_mode = "workspace-write"

[sandbox_workspace_write]
network_access = false
```

### 呼び出し規約

```bash
# レビュー(G2 / G4)
codex exec --model "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"" --sandbox read-only \
  "$(cat agent_docs/codex/review_blind.md)

  ## Issue
  $(cat /path/to/issue.md)

  ## Diff
  $(git diff main...HEAD)"

# 実装(worktree 内で)
cd ../wt-m2-1 && codex exec --model "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"" --sandbox workspace-write \
  "$(cat agent_docs/codex/implement_issue.md)

  ## Issue
  ..."
```

`~/.codex/reviewer.config.toml` / `~/.codex/engineer.config.toml` が存在する環境では、上記に `--profile reviewer` / `--profile engineer` を追加してよい。profile ファイルが無いことは blocker ではない。

プロンプト本文は必ず `agent_docs/codex/` のテンプレートから組み立てる。場当たりのプロンプトで Codex を呼ばない(観点の抜けとブラインド規律の破れを防ぐ)。

### Codex pre-flight

Codex を実運用する前に、実行環境ごとに以下を確認する。特に GitHub / git 初期化前後で project config の trust/load が変わり得るため、M0 開始前と draft PR 運用開始前に再確認する。

```bash
codex --version
codex doctor --json
codex exec --strict-config --help
codex debug models --bundled

codex exec --model "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"" --sandbox read-only --skip-git-repo-check \
  "Pre-flight only. Do not edit files. Report startup success."

codex exec --model "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"" --sandbox workspace-write --skip-git-repo-check \
  "Pre-flight only. Do not edit files. Report startup success."
```

確認項目:

- `codex --version` が想定 CLI バージョンである
- `codex doctor --json` で auth / config / sandbox / network が想定どおりである
- `codex debug models --bundled` または実カタログで、その task に選んだ model が必要な effort をサポートする
- `--profile reviewer` / `--profile engineer` は、対応する `$CODEX_HOME/<name>.config.toml` がある場合だけ解決確認する。未作成なら `--profile` を省略して運用する
- `--sandbox read-only` / `--sandbox workspace-write` を明示して起動できる
- trusted project として repo 内 `.codex/config.toml` が読み込まれる。読まれない場合も、CLI flags により model / reasoning / sandbox は担保する

## 7. Hook / pre-commit 方針

Hook と pre-commit 的な自動フィードバックは、プロンプト指示を補強する決定論的ハーネスとして扱う。M0 では方針化まで、M1 以降に shell + Go 標準ツール中心で段階導入する。

- PostToolUse: Go ファイル編集後に対象ファイルへ `gofmt` をかける候補
- Stop: 完了宣言前に `gofmt_out="$(gofmt -l .)" && test -z "$gofmt_out"` / `go test ./...` などの短い検証を促す候補
- PreToolUse: `AGENTS.md` / `docs/implementation_plan.md` / `docs/adr/` の変更、破壊的コマンド、`go.mod` 変更を警告または停止する候補
- pre-commit: git 初期化後、依存追加・`sh -c`・レイヤ違反・UI 文字列直書きなどの構造チェックを CI と同じ入口で走らせる候補

Hook は人間承認事項やセキュリティ境界を置き換えない。通過判定の正は G1/CI/受け入れ条件とする。

## 8. Codex プロンプトテンプレート一覧(`agent_docs/codex/`)

| ファイル | 用途 | プロファイル | 対応ゲート |
|---|---|---|---|
| `review_blind.md` | 独立コードレビュー(結論非開示) | reviewer | G2 |
| `review_adversarial.md` | 攻撃者視点のセキュリティレビュー | reviewer | G4 |
| `design_alternative.md` | 同一課題への独立設計案 | reviewer | G0 |
| `implement_issue.md` | Issue 実装(受け入れ条件駆動) | engineer | — |
| `rescue.md` | 行き詰まりブランチの引き取り再実装 | engineer | — |
| `qa_exploratory.md` | スタブ環境での探索的 QA | engineer | リリース前 |
| `docs_review.md` | 手順書・README の第三者検証 | reviewer | M4-4 |

## 9. セッション開始ルーチン

各セッション開始時、実装・文書編集に入る前に以下を確認する。

1. 作業ディレクトリと git 状態(`pwd`, `git status`。git 未初期化ならその事実を確認)
2. 対象タスクの Scope / Acceptance criteria
3. `docs/notes.md` の関連する実機確認項目
4. 人間承認が必要な操作に該当しないか
5. 直前のレビュー・未解決コメント・ADR の有無

## 10. 1 Issue の標準フロー

```
1. Orchestrator: Issue と受け入れ条件を確認、曖昧なら人間に質問
2. (G0 対象なら) Plan で計画 + Codex design_alternative → 人間承認
3. 実装(Orchestrator 本体 or engineer subagent @ worktree)
4. G1: verifier で検証 4 コマンド
5. Draft PR 作成(bootstrap 期は代替ログ先を決める)
6. G2: codex-reviewer でブラインドレビュー
7. G3: Orchestrator が統合・修正・再 G1、全指摘の扱いを PR コメント/説明に記録
8. (G4 対象なら) adversarial review
9. PR 説明を更新(検証結果・レビューログ・手動確認手順を含む)
10. CI グリーン確認
11. G5: 人間がレビュー・マージ
```
