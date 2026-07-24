# Hardcore Together デプロイ構成の設計方針

このリポジトリ（`hardcore-together-deploy`）が`docker-compose.yml`一式をどう組み立てているか、なぜそうしているかをまとめる。`specification.md`・`architecture-gate.md`・`architecture-manager.md`・`architecture-neoforge.md`が各コンポーネント自体の仕様・設計を定義するのに対し、こちらは「それらを実際にどう1つのdocker-compose構成へ組み上げるか」というデプロイ側の設計を扱う。

## 1. 全体構成

| コンポーネント | 配布形態 | このリポジトリでの扱い |
|---|---|---|
| Gate | Dockerイメージ（リリース） | `docker-compose.yml`でそのまま起動 |
| Manager | Goバイナリ（リリース） | `./manager/Dockerfile`が`itzg/minecraft-server:java21`をベースにリリースバイナリを組み込んでビルドする |
| Hardcore Together（hardcoreサーバーのMOD） | `.jar`（[リリース](https://github.com/RayLight1732/hardcore-together-neoforge/releases)） | コンテナ起動時に自動取得 |
| Parkour Lobby（lobbyサーバーのMOD） | `.jar`（リリース） | mods/への手動配置のみ（GitHub Releases未確認のため自動取得の対象外） |
| PCF（Proxy-Compatible-Forge） | `.jar`（[リリース](https://github.com/adde0109/Proxy-Compatible-Forge/releases)） | hardcore・lobby双方でコンテナ起動時に自動取得 |
| Kotlin for Forge | GitHub Releases無し、[Modrinth](https://modrinth.com/mod/kotlin-for-forge)にあり | hardcoreでitzg組み込みの`MODRINTH_PROJECTS`により自動取得 |

hardcoreサーバー本体は単独コンテナではなく、Managerコンテナ内で`os/exec`の子プロセスとして起動される（`specification.md` 1節「Manager・hardcoreサーバーは同一コンテナ上で動作する必要がある」）。

## 2. Managerコンテナがitzg/minecraft-serverをベースにしている理由

hardcoreサーバーがManagerの子プロセスとして動く以上、Managerコンテナ自身がNeoForgeサーバーのインストール・実行環境を持つ必要がある。単にJREを積んで自前でNeoForgeインストーラーを呼ぶ代わりに、itzgの起動スクリプト（`/start`）をManagerの`hardcore.startCommand`としてexecさせることで、NeoForgeサーバー本体のインストール・EULA同意・`server.properties`の基本項目の適用をitzgに任せている。

itzgのエントリポイントは`start → start-configuration → start-deployNeoForge → … → start-finalExec`まですべて`exec`（プロセスイメージの差し替え、PIDは同一のまま）でチェーンしており、最終的に`exec mc-server-runner -- java ...`に行き着く。そのためManagerが`os/exec`で掴んだPIDは起動から終了まで一貫し、Managerの`SIGTERM`は`mc-server-runner`（RCON経由で`stop`コマンドを送りグレースフルシャットダウンさせる）に正しく届く。これは実際にitzgのソースを読み、以下を検証して確認した：

- 実プロセスツリー：`manager(PID1) → mc-server-runner(minecraftユーザー) → bash run.sh → java`
- `docker stop`でManagerへSIGTERMを送った際、ワールド保存・チャンク保存を経て数秒でクリーンに終了すること（`exitcode=0`）
- `hardcoretogether.jar`・PCFのjar・Kotlin for Forgeを自動取得させ、NeoForgeサーバーが3つとも正しくロードして`Done`まで起動すること、`ready`→`hardcore-ready`が実際にGate側（テスト用の疑似クライアント）まで届くこと

## 3. UID/GID・権限降格の設計

Managerコンテナ・lobbyコンテナのentrypointは、どちらもitzg自身の`scripts/start`と同じ権限降格パターンを踏襲している（itzgの実ソースを確認して判明）：

```sh
if [ "$(id -u)" = 0 ]; then
  # /dataトップレベルの所有者が既定と違えばchown -R
  # そのあとgosuで降格
  exec gosu ${runAsUser}:${runAsGroup} 続きの処理...
else
  # 既に非rootならこの降格処理自体をスキップしてそのまま続行
  続きの処理...
fi
```

`docker-compose.yml`は`manager`・`lobby`双方の`environment`に`UID`/`GID`（既定`1000:1000`、itzg自身の既定と一致）を明示している。entrypoint.shはroot（コンテナの初期状態）で起動した直後、この値を使って

1. `/data`（bind mount）のトップレベル所有者が一致していなければ`chown -R`
2. `manager`の場合は`/app/archive`（`manager-archive`という別の名前付きボリューム。Dockerが新規作成する際はroot:root）も同様にトップレベルチェック
3. `exec gosu <uid>:<gid> "$0" "$@"`で自分自身を非rootとして再実行

の順で1回だけ行い、以降（mod自動取得・NeoForgeの先回りインストール・Manager本体の起動）はすべて非rootのまま実行する。

**この設計に至った経緯**：当初は「entrypoint.shはroot権限のまま最後まで動かし、`mods/`や新規作成物だけを後から個別に`chown`する」という実装だった。しかしこの方式は、新しく作られるファイル・ディレクトリが増えるたびに対応するchown処理を追加し続ける必要があり（NeoForgeの先回りインストールを追加した際は、実行前後の`/data`直下の差分を`comm`で検出してchownする、という複雑な実装になった）、しかもこの`comm`にロケール依存の`sort`を渡してしまい、`comm`が「ソート順がおかしい」とエラーを返し、`set -e`+`pipefail`によりentrypoint.sh全体が異常終了してコンテナが無限リスタートするという実障害も起きた。「rootでいるのは最初の`chown`確認だけにして、それ以降は全部非rootで動かす」という今の設計にしたことで、新しく作るものが増えても個別のchown対応が一切不要になった。

## 4. NeoForgeの先回りインストール

hardcoreサーバーはManagerが受け取った明示的なGateコマンド（`/start`等）でしか起動しない（自動起動しない）ため、何も手を打たないと初回の実際の`/start`でNeoForgeの完全なダウンロード・インストールが走り、非常に時間がかかる。

`manager/entrypoint.d/prewarm-neoforge.sh`は、itzg自身が`start-deployNeoForge`内部で使っているのと同じツール（`mc-image-helper install-neoforge`）を、コンテナ起動時に先回りして呼び出す。このツールはインストール成功時にバージョンマニフェストを書き込み、次回以降は同じマニフェストと現在のバージョン設定が一致するかどうかだけで「インストール済み」と判定してスキップする。そのため、先回りで書いたマニフェストを実際の`/start`時にitzg自身の`start-deployNeoForge`がそのまま見つけて再利用でき、即座に起動へ進める。

（当初は別の実装方法として、NeoForge公式インストーラーを直接叩く案も検討したが、これだとitzg独自のマニフェスト形式で書かれないため、実際の`/start`時にitzgが「未インストール」と誤認し、結局フルインストールをやり直してしまう。itzgと同じツールを直接呼ぶことで、この二重インストールを避けている。）

## 5. mod自動取得

`hardcoretogether.jar`・PCFのjarはGitHub Releasesで配布されているが、Modrinth/CurseForgeには無いためitzg組み込みの自動取得（`MODRINTH_PROJECTS`等）の対象外。`manager/entrypoint.d/fetch-mods.sh`・`lobby/entrypoint.sh`がGitHub Releases APIを直接叩いて解決・ダウンロードする（`HARDCORETOGETHER_VERSION`・`PCF_VERSION`環境変数で`latest`または特定タグを指定、解決結果が前回と変わらなければ再ダウンロードしない）。

`hardcoretogether.jar`はKotlin製で、単体では読み込めず"Kotlin for Forge"（言語プロバイダ、GitHub Releasesは無いがModrinthにはある）が別途必要。これは実際に起動して`Mod File hardcoretogether.jar needs language provider kotlinforforge:5.3 or above to load`というエラーで発覚し、itzg組み込みの`MODRINTH_PROJECTS`環境変数で解決した。

`parkourlobby.jar`のみGitHub Releasesが未確認のため自動取得の対象外とし、初回のみ`data/lobby/mods/`への手動配置が必要。

## 6. server.propertiesの扱い

`data/manager/hardcore/server.properties`は事前に用意する必要がない。itzgが`HARDCORE`等の環境変数から起動時に自動生成する。

以前はManager自身の`EnsureHardcoreMode()`が`/start`のたびにこのファイルを事前に読み取り検証しており、itzgが本格的な`server.properties`を生成するより前に走るチェックだったため、`data/`の外に置いた最小シード（`manager/server.properties.seed`）をentrypoint.shがこのファイルの不在時のみコピーする仕組みが必要だった。この検証自体がManager側の方針変更で無くなったため、シードの仕組みごと削除した。

## 7. Gate設定と秘密情報の扱い

`gate/config.yml`の`hardcoreTogether.admins`にはOPプレイヤーの実UUID（個人を特定できる情報）を書く必要があるため、このファイル自体は`.gitignore`対象にしている。コミット対象は`gate/config.yml.example`（プレースホルダーUUID入りのテンプレート）のみとし、セットアップ時に実ファイルへコピーして実UUIDを設定する運用にしている。

`GATE_VELOCITY_SECRET`（Gate⇔lobby/hardcore間のVelocity方式forwarding共有シークレット）も同様に`.env`（gitignore対象）で管理し、`.env.example`にはプレースホルダーのみ置く。

PCFの`approvedProxyHosts`は空リストのままにしている。空＝制限なし（PCFの実ソース`ModernForwarding.java`で確認済み）であり、シークレットさえ漏れなければIPアドレスによる追加の制限は不要と判断した。

## 8. 検証中に見つけた不具合

- **MOD⇔Manager接続ポートの不一致**：`hardcoretogether.jar`は接続先ポートを設定ファイルではなくJVMシステムプロパティ`hardcoretogether.gate.port`（デフォルト`25585`）でしか変更できない（MOD側のソース`TcpGateConnection.kt`を確認して判明）。`manager/config.yml`の`signalPort`を`architecture-manager.md`のサンプルにあった`9001`のままにしていたため、実際に起動するとMODが`Connection refused`を繰り返してManagerに接続できず、`ready`が届かなかった。`signalPort`をMOD側のデフォルトである`25585`に合わせて解決した。
- **`Unable to verify player details.`（PCFのforwarding検証エラー）**：secretの不一致を疑ったが実際は一致しており、原因は別にあった（詳細調査は継続中の可能性あり、新しいシークレットへの入れ替えとgate/lobby/manager再起動で解消した実績あり）。

## 9. 未確定・要確認事項（specification.md 10節・各architectureドキュメント14節・6節より抜粋）

- PCFの具体的なバージョン・設定項目
- NeoForge・Minecraftの具体的なバージョン（`docker-compose.yml`のmanager/lobby両サービスの`NEOFORGE_VERSION`・`VERSION`を要固定）
- Manager⇔Gate間・MOD⇔Manager間の接続タイムアウト・リトライ回数
- ボスMobのチェックポイント系/挑戦終了系の最終分類（`hardcoretogether`のMOD設定ファイル側で対応）
