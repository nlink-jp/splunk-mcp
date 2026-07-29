# splunk-mcp

Splunk の検索を REST API 経由で提供するローカル MCP サーバー。**確定件数と
全件取得の保証**を最優先に設計したデータ分析向けツールです。

[English README is here](README.md)

## なぜ作ったか

Splunk 公式の MCP Server アプリ（Splunk 側にインストールする方式)は、検索を
`exec_mode=oneshot` + 60秒タイムアウトで実行し、SPL に `| head N`（デフォル
ト上限 1000 行）を自動注入します。件数は安定せず、上限到達時は近似値しか返
らず、全件を取得する手段がありません — データ分析には使えず、エージェント
のリトライループの原因にもなります。

splunk-mcp はすべての検索を**非同期 Splunk ジョブ**として実行します（ジョブ
作成 → `DONE` までポーリング → 確定 `resultCount` 取得 → `/results` を
ページング取得）。これにより:

- `total_rows` は常に確定した最終件数 — プレビューや近似値は一切なし
- 大規模な結果は**決して切り捨てない**: 呼び出し側指定の `workspace_root`
  配下に JSONL ファイルとして全件書き出し、先頭プレビューを添付
- 長時間検索もタイムアウトしない: `run_query` は `wait_timeout` 時に SID を
  返し、ジョブはサーバー側で実行継続
- Splunk 側へのアプリインストール不要 — トークンだけで接続可能

## ツール

| ツール | 用途 |
|---|---|
| `run_query` | SPL を実行し完了まで待機、確定件数 + 結果を返す |
| `start_query` | SPL を非同期実行開始し SID を即返す |
| `check_job` | SID でジョブ状態・件数を確認 |
| `get_results` | 完了ジョブの結果取得（offset/count ページング） |
| `cancel_job` | 実行中ジョブのキャンセル |
| `get_usage` | エージェント向けツールリファレンス |

### 結果の返し方

インライン閾値（デフォルト 100 行）以下の結果はそのまま JSON で返します。
超えた場合は**全行**を JSONL ファイル（1 行 1 JSON オブジェクト）として
`workspace_root` 配下に書き出し、レスポンスにはファイルパス・先頭 5 行の
プレビュー・確定 `total_rows` を含めます。書き出したファイルは
[data-toolbox-mcp](https://github.com/nlink-jp/data-toolbox-mcp) にそのまま
読み込んで分析を継続できます。

### SPL ガード

書き込み・削除系コマンド（`delete`, `collect`, `mcollect`,
`meventcollect`, `outputlookup`, `outputcsv`, `sendemail`,
`runshellscript`, `script`）はデフォルトで拒否し、構造化エラー
`unsafe_spl` を返します。`[server] allow_commands` で個別に許可できます。
最終的な防衛線は Splunk 側の RBAC です。

## インストール

リリースページからビルド済みバイナリをダウンロードするか、ソースから:

```bash
git clone https://github.com/nlink-jp/splunk-mcp.git
cd splunk-mcp
make build
# バイナリ: dist/splunk-mcp
```

## 設定

**1 サーバーインスタンス = 1 接続先。** 複数の Splunk に接続する場合は
接続先ごとに config ファイルを作り、別名で複数登録します:

```json
{
  "mcpServers": {
    "splunk-prod": { "command": "splunk-mcp", "args": ["--config", "/path/to/prod.toml"] },
    "splunk-dev":  { "command": "splunk-mcp", "args": ["--config", "/path/to/dev.toml"] }
  }
}
```

[config.example.toml](config.example.toml) を
`~/.config/splunk-mcp/config.toml`（デフォルトパス）にコピーして設定:

```toml
[splunk]
host  = "https://your-splunk.example.com:8089"
token = "your-token"
# insecure = false          # 自己署名証明書の場合
# prepend  = "pipe-only"    # auto | pipe-only | off（splunk-cli と同じ3モード）

[server]
# inline_row_threshold = 100
# job_ttl              = "10m"
# allow_commands       = []
```

```bash
chmod 600 ~/.config/splunk-mcp/config.toml
```

config の解決順: `--config` フラグ → `$SPLUNK_MCP_CONFIG` →
`~/.config/splunk-mcp/config.toml` → `./config.toml`。接続設定は環境変数
（`SPLUNK_HOST`, `SPLUNK_TOKEN`, `SPLUNK_USER`, `SPLUNK_PASSWORD`,
`SPLUNK_APP`）がファイルより優先されます — splunk-cli と同じ変数名なので
認証情報を共用できます。

## 使い方

```bash
splunk-mcp                    # stdio で MCP サービス開始（デフォルト）
splunk-mcp serve --config /path/to/prod.toml
splunk-mcp --version
```

エージェントの典型的なワークフロー:

- **クイック分析** — `run_query` に SPL を渡す。`wait_seconds`（デフォルト
  300 秒）以内に完了すれば確定件数付きで結果が返る。
- **長時間検索** — `start_query` → `check_job` をポーリング → `get_results`。
- **大規模結果** — `workspace_root`（絶対パス）を渡すと全件が JSONL ファイル
  + プレビューで届く。行が失われることはない。

## 運用メモ

- 完了ジョブは TTL 経過で消える（Splunk デフォルトは数分。`[server]
  job_ttl` で延長可）。期限切れ SID は `job_not_found` を返す。
- Splunk はロール毎に同時実行サーチ数を制限している。大量並列より
  `start_query` の逐次バッチを推奨。
- ツールエラーは構造化 JSON `{code, message, details}`。エラー回復表は
  `get_usage` を参照。

## 開発

```bash
make test    # go test ./...
make vet     # go vet ./...
make check   # vet + test + build
make build   # dist/splunk-mcp に出力
```

## ライセンス

MIT — [LICENSE](LICENSE) を参照。
