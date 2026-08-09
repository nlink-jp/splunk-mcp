# RFP: splunk-mcp

> Generated: 2026-07-30
> Status: Draft

## 1. Problem Statement

Splunk 公式 MCP Server アプリ（Splunk 側にインストールする方式）は、`exec_mode=oneshot` の同期実行 + HTTP 60秒タイムアウト + SPL への `| head N` 自動注入（デフォルト上限 1000 行）という設計のため、結果件数が安定せず、上限到達時は近似値（`approx_total: "1000+"`）しか返せない。正確な総件数の取得や全件取得の手段が構造的に存在せず、データ分析用途に耐えない。またエージェントが不安定な結果に対してリトライを繰り返す原因にもなる。

splunk-mcp は Splunk REST API を非同期ジョブパターンで直接呼び出すローカル MCP サーバーとして、**確定件数と全件取得を保証**する。対象ユーザーは Claude Code / MCP クライアントから Splunk のデータ分析を行う自分自身。

## 2. Functional Specification

### Commands / API Surface

MCP ツール（Phase 1 コア）:

| ツール | 動作 |
|---|---|
| `run_query` | SPL 実行。内部は `exec_mode=normal` でジョブ作成 → `dispatchState=DONE` までポーリング → 確定 `resultCount` + 結果返却。oneshot / results_preview は一切使わない |
| `start_query` | ジョブを開始し SID を即返す（長時間検索用） |
| `check_job` | dispatchState / 進捗 / 確定件数の確認 |
| `get_results` | 完了ジョブから結果取得（offset / count ページングで全件吸い上げ） |
| `cancel_job` | ジョブのキャンセル |
| `get_usage` | セルフドキュメント（nlink-jp MCP 標準） |

Phase 2 で追加する探索系ツール:

| ツール | 動作 |
|---|---|
| `list_indexes` | インデックス一覧（イベント数・時間範囲付き） |
| `list_sourcetypes` | sourcetype 一覧（対象 index 指定可） |
| `list_saved_searches` | 保存済みサーチの一覧 |
| `run_saved_search` | 保存済みサーチの実行（結果返却は run_query と同一機構） |

### Input / Output

- 入力: SPL 文字列、時間範囲（earliest / latest、Splunk time modifier 形式）、app コンテキスト、行数閾値、workspace_root
- 出力（結果返却の2形態）:
  - 結果が閾値（デフォルト 100 行、config / ツール引数で変更可）以下 → インライン JSON
  - 閾値超え → `workspace_root` 配下に JSONL で**全件**書き出し、`results_file` パス + 先頭プレビュー + 確定件数を返す（切り捨てなし。書き出したファイルは data-toolbox-mcp でそのまま分析継続可能）
- ツールエラーは構造化 JSON（`{code, message}`、nlink-jp MCP 規約）

### Configuration

**1 MCP インスタンス = 1 接続先。** プロファイル機構は持たず、接続先ごとに config ファイルを分けて MCP サーバーを別名で複数登録する。

```toml
# 例: ~/.config/splunk-mcp/prod.toml
[splunk]
host  = "https://splunk-prod.example.com:8089"
token = "..."
# app = "search"
# insecure = false
# http_timeout = "30s"
# prepend = "pipe-only"   # auto | pipe-only | off（splunk-cli と同一の3モード）

[server]
inline_row_threshold = 100
# job_ttl = "10m"
```

```json
// MCP クライアント側の登録例
{
  "splunk-prod": { "command": "splunk-mcp", "args": ["--config", "~/.config/splunk-mcp/prod.toml"] },
  "splunk-dev":  { "command": "splunk-mcp", "args": ["--config", "~/.config/splunk-mcp/dev.toml"] }
}
```

- config パスは `--config` フラグまたは env var で指定（デフォルト `~/.config/splunk-mcp/config.toml`）
- 優先順位は splunk-cli と同一: フラグ → env var（`SPLUNK_HOST` / `SPLUNK_TOKEN` 等）→ config ファイル
- config ファイルは 600 パーミッション警告（splunk-cli と同一挙動）

### External Dependencies

- Splunk Enterprise / Splunk Cloud の REST API（管理ポート 8089）
- 認証: Bearer トークン（推奨）または Basic 認証
- Splunk 側へのアプリインストールは**不要**（公式 MCP アプリに対する主要な運用上の優位点）

## 3. Design Decisions

- **言語: Go** — splunk-cli の資産（REST クライアント、認証、prepend 正規化）を流用でき、既存 nlink-jp MCP 群と同一のビルド・署名・リリース基盤に乗る
- **骨格: data-toolbox-mcp から移植** — 新規 MCP の標準手順（get_usage、構造化エラー、ファイル媒介パターン）
- **splunk-cli とのコード共有: コピー移植** — splunk-cli の internal パッケージを splunk-mcp にコピーして独立メンテ。即着手可能でリリース単位も独立。REST 層は安定しており乖離リスクは低いと判断（共有ライブラリ切り出しはリポ3つの同期リリースコストが見合わない）
- **マルチインスタンス: config パス切替型** — 1インスタンス = 1接続先。MCP 登録名で接続先を使い分ける。プロファイル機構やツール引数 `profile` は持たない（ツール面がシンプルになり、SID とインスタンスの対応が自明になる）
- **安全ガード: 破壊系のみブロック** — `| delete`、`| collect`、`| outputlookup` 等の書き込み・削除系コマンドをデフォルト拒否、config で個別 allow 可能。読み取り・分析系は無制限。公式アプリの safe_spl allowlist 方式はマルチテナント前提の設計であり、シングルユーザー用途では保守コストが見合わない。最終防衛線は Splunk 側 RBAC（トークンのロール権限）
- **明示的スコープ外**: SPL2 対応（公式アプリの SPL2→SPL1 コンパイルは不要）、OAuth / SSO、マルチテナント・ガードレール（rate limit、tool roles 等）、リアルタイムサーチ、Splunk 側アプリとしての配布

補完関係: splunk-cli（人間の手元操作）と splunk-mcp（エージェント操作）が同一の REST 層設計を共有する対の関係。大規模結果は data-toolbox-mcp のワークスペースに接続して分析を継続できる。

## 4. Development Plan

### Phase 1: Core

- data-toolbox-mcp から MCP 骨格移植、splunk-cli から REST クライアント / 認証 / prepend 正規化をコピー移植
- コアツール6種（run_query / start_query / check_job / get_results / cancel_job / get_usage）
- 非同期ジョブパターン（ジョブ作成 → ポーリング → 確定件数 → ページング全件取得）
- ファイル媒介返却（閾値、JSONL 書き出し、プレビュー）
- 破壊系コマンドガード
- テスト: モック HTTP サーバーで REST 層・ジョブライフサイクル・ガード・閾値分岐を検証

### Phase 2: Features

- 探索系ツール4種（list_indexes / list_sourcetypes / list_saved_searches / run_saved_search）
- Phase 1 と独立にレビュー可能

### Phase 3: Release

- docs/{en,ja} 整備、README.md / README.ja.md / CHANGELOG.md / AGENTS.md
- 実 Splunk インスタンスでの実データ E2E（リリース前必須）
- 署名 + notarize、リリース12ステップ、umbrella submodule 更新、check-org.sh

## 5. Required API Scopes / Permissions

- Splunk Bearer トークン（または Basic 認証）のみ
- 必要権限: search capability + 対象 index への読み取り権限。RBAC はトークンに紐づくロールに全面的に従う
- OAuth スコープ / IAM ロール: None

## 6. Series Placement

Series: **util-series**

Reason: nlink-jp の MCP サーバー群（data-toolbox-mcp、voice-studio-mcp、pcap-analyzer-mcp 等）は util-series に集約されており、この事実上の規律に従う。cli-series は「Interactive CLI clients」の定義から MCP サーバーは対象外（splunk-cli とは対の関係だが同居はしない）。

## 7. External Platform Constraints

- 管理ポート 8089 経由の REST API。自己署名証明書環境が多いため `insecure` オプションを継承
- サーチジョブ TTL: デフォルトでは完了後数分で自動削除される。`get_results` は TTL 内に呼ぶ必要があり、ジョブ作成時に `ttl` パラメータで延長可能
- サーバー側 `limits.conf` の制約: 1リクエストで取得できる結果行数に上限があるため、全件取得は offset / count ページングで吸収する
- ロール毎の同時実行サーチ数クォータ: 並列エージェントが多数のジョブを同時に走らせると枯渇しうる（get_usage に注意書きを含める）
- Splunk Cloud の場合、管理ポートへのアクセスに IP allowlist 設定が前提
- ジョブ API は v1 エンドポイント（`services/search/jobs`）を使用（**実装時修正**: 当初 v2 前提としたが、splunk-cli で実機検証済みの v1 に統一。v2 のジョブ status/control 対応はバージョン依存の不確実性があり、v1 なら Splunk 9.x 限定の制約も消える）

---

## Discussion Log

- **背景**: 手元に clone した公式 Splunk MCP Server アプリ（`oss/Splunk_MCP_Server`）のソースを確認し、oneshot 実行（60秒タイムアウト）+ `| head N` 注入（max_row_limit 1000）+ 上限到達時の近似件数返却が実装上の事実であることを確認。件数不安定はバグではなく設計で、外部から修正不可能と判断し、REST API 直叩きのローカル MCP への完全焼き直しを決定
- **ツール名**: splunk-mcp（splunk-cli と対になる最短名。スコープ拡張しても破綻しない）を採用。splunk-search-mcp / splunk-query-mcp は不採用
- **スコープ**: クエリ実行コアに加え、メタデータ探索と saved search 実行もフル構成で採用（探索系は Phase 2 に分離）
- **安全ガード**: 「破壊系のみブロック」を採用。「ガードなし（RBAC 全面依存）」「読み取り専用 allowlist」は不採用（後者は公式アプリと同じ allowlist 保守コスト問題を抱え込むため）
- **結果返却**: 閾値分岐（インライン / ファイル媒介）を採用。「常にファイル書き出し」は小規模結果でのオーバーヘッドを理由に不採用
- **コード共有**: コピー移植を採用。lib-series への共有ライブラリ切り出しは、リポ3つの同期リリース運用コストを理由に不採用
- **ジョブ API バージョン**: Phase 1 実装時に v2（`search/v2/jobs`）から v1（`services/search/jobs`）へ変更。splunk-cli で実機実績のあるパスを優先し、Splunk 9.x 限定制約を除去
- **マルチインスタンス**: 当初プロファイル方式（`[profiles.<name>]` + ツール引数 `profile`）を提案したが、ユーザー判断で **config パス切替型（1 MCP インスタンス = 1 接続先）** に確定。MCP 登録名で接続先を使い分け、ツール面をシンプルに保つ
