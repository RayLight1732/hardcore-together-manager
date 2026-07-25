# Hardcore Together 内部アーキテクチャ設計

`hardcoretogether`モジュールのコード構成に関する設計方針。`specification.md`が「何をするMODか」（外部仕様）を定義するのに対し、こちらは「コードをどう組むか」（内部構造）を定義する。

## 背景：既存コードの何が問題か

- **`GateClient`・`ElapsedTimeTracker`・`DeathHandler`がミュータブルな状態を持つ`object`（シングルトン）になっている。** `accumulatedNanos`・`countdownActive`・`socket`のような、実行中のサーバーインスタンスに紐づくべき状態が、Javaのstatic変数のようにグローバルに置かれている。
- **`server: MinecraftServer`をほぼ全関数の第一引数として引き回している。** `RecordStore.load(server, challengeId)`のように、本来インスタンスが持つべき依存を毎回パラメータで渡している。
- **`RecordEvent`が1つのdata classにnullableフィールドを詰め込んだ「なんちゃって直和型」になっている。** `save`/`death`/`clear`で実際に使うフィールドが異なるのに全部nullableで同居し、型で不正な組み合わせを排除できない。
- **判断ロジックを1つのクラスに集約すると肥大化するリスクがある。** tick処理・死亡処理・チェックポイント記録・クリア記録・アーカイブ実行・保存・状態更新を1クラスに持たせると、UseCase・Service・Facadeを兼務する巨大クラスになる。

## 全体構成：domain / application / port / adapter の4層

```
com.ray.light.hardcoretogether/
├── HardcoreTogether.kt              … NeoForgeエントリポイント（合成ルート）。objectのまま
│
├── domain/                          … 純粋Kotlin。Minecraft/NeoForge/Gson/portへのimportゼロ
│   ├── Challenge.kt                 … idと経過時間を持つエンティティ
│   ├── StructuredValue.kt           … JSON非依存の汎用木構造（Str/Num/Bool/Arr/Obj）。TriggerとRecordEventが自己記述に使う
│   ├── Trigger.kt                   … 自分の構造をStructuredValueとして語れるinterface＋実装クラス群
│   ├── BossCategory.kt
│   └── RecordEvent.kt               … sealed class（Save/Death/Clear）。Triggerを保持し、自分自身もStructuredValueとして語れる
│
├── application/                     … domain(+port)に依存する。「何が起きたらどうするか」の判断・調整
│   ├── ChallengeApplicationService.kt … 薄いオーケストレーション
│   ├── ChallengeService.kt          … Challengeの生成・tick・終了、ChallengeStateとの同期
│   ├── ArchiveService.kt            … アーカイブ名の採番＋ArchiveGatewayの呼び出し
│   └── RecordService.kt             … RecordEventの組み立てとRecordRepositoryへの保存
│
├── port/                            … application層が外の世界に要求するインターフェース
│   ├── RecordRepository.kt          … 書き込み専用（一覧の読み取りはGateが直接ファイルを読むため持たない。下記参照）
│   ├── ArchiveGateway.kt            … save-off/flush/archive-request/save-on + ready/running-changed送信
│   └── ChallengeState.kt            … running/challengeIdの読み書き
│
└── adapter/                         … 各portの実装＋NeoForge/Gson/ソケットとの実際の接続
    ├── neoforge/
    │   ├── SavedDataChallengeState.kt  … port.ChallengeStateの実装（NeoForge SavedDataを包む）
    │   ├── DeathEventListener.kt       … @SubscribeEvent → DeathCountdownへ委譲するだけ
    │   ├── DeathCountdown.kt           … 死亡カウントダウン・ボスキル判定（NeoForge固有の可変状態を持つインスタンス）
    │   ├── TickListener.kt
    │   ├── HardcoreConfig.kt           … ModConfigSpec（bosses.checkpoint/clear + storage.recordsDir）
    │   ├── CommandRegistrar.kt         … /archive のみ登録（下記参照）
    │   └── Runtime.kt                  … 合成ルートのサーバーセッション単位バンドル（下記参照）
    ├── file/
    │   ├── FileRecordRepository.kt     … port.RecordRepositoryの実装。`dir: Path`を取る（MinecraftServerではない）
    │   └── StructuredValueGson.kt      … StructuredValue→Gson JsonElementの汎用変換のみ（書き込み方向）。Trigger/RecordEventごとの知識は持たない
    └── gate/
        ├── TcpGateConnection.kt        … NDJSONの配線のみ（TCPソケット・送受信）。MinecraftServer非依存
        └── ArchiveGatewayImpl.kt       … port.ArchiveGatewayの実装。TcpGateConnection＋save-off/save-onブラケットを合成する
```

**hardcore MODが直接登録するプレイヤー向けコマンドは`/archive`のみ**（specification.md 4.2節）。`/start`・`/load`はhardcoreが未起動でも呼べる必要があるためGateレベル（2.1節）、`/savedata`・`/senpan`はGateが`records/`を直接読み取る（2.6節）ためhardcore MODを経由しない。一方`/archive`は稼働中のMinecraftサーバープロセスでしか実行できない操作（オートセーブの一時停止）を伴うため、「hardcoreに接続していなくても実行できる」利点が実質的に無く、Gate中継は複雑さに見合わないと判断し従来通りhardcore MODの直接コマンドのまま維持した（検討の経緯は下記決定ログ参照）。

### 各層の責務

- **domain**：ゲームルールの語彙そのもの（Challenge・Trigger・RecordEvent）。他の層に一切依存しない。portすら知らない
- **application**：domainのエンティティを操作し、portを通じて外の世界とやり取りする「使うケース」の実装。ここが唯一portに依存する層
- **port**：applicationが「外の世界に何をしてほしいか」を宣言するインターフェース。依存方向を逆転させる境界線
- **adapter**：portの実装＋NeoForgeやGson、ソケットとの実際のやり取り。フレームワーク都合のコードはすべてここに閉じ込める

`HardcoreConfig`（`ModConfigSpec`）はNeoForge依存が避けられない性質のものなので`adapter/neoforge`側に置く。NeoForgeはMOD1つにつきCOMMON設定ファイル1つのため、ボス設定（`bosses.*`）とストレージ設定（`storage.recordsDir`）を1つの`ModConfigSpec`にまとめている（TOML上は`[bosses]`/`[storage]`でセクション分け）。NeoForge自身の`net.neoforged.fml.config.ModConfig`という型名と衝突するため、単純に`ModConfig`とは名付けていない。

### なぜdomainとapplicationを分けるか

判断ロジック（tick・死亡・チェックポイント・クリア・アーカイブ実行・保存・状態更新）の置き場所には2つの選択肢がある。

| 選択肢                         | 内容                                                                                                                                                         | トレードオフ                                                                                                                                                                                                                                                     |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A: 単一クラス                  | domainパッケージに置いた1クラスがportに直接依存し、全判断ロジックを持つ                                                                                      | 依存性逆転の原則自体には反しないが、全責務が1クラスに集まるためUseCase・Service・Facadeを兼務する巨大クラスになりやすい。また「domain」という名前のパッケージにportへ依存するクラスが混在し、純粋なdomain（Challenge・Trigger・RecordEvent）との境界が曖昧になる |
| B: application層で分割（採用） | `application`層を新設し、`ChallengeService`/`ArchiveService`/`RecordService`の3つに責務を分割。全体の調整だけを行う`ChallengeApplicationService`を薄く被せる | クラス数は増えるが、各クラスが単一責務に留まる。真のdomain（Challenge/Trigger/RecordEvent/BossCategory）はportに一切依存しない、より厳密な境界になる                                                                                                             |

## `Challenge`：idと経過時間を持つドメインエンティティ

```kotlin
// domain/Challenge.kt
class Challenge(
    val id: String,
    running: Boolean,
    elapsedSeconds: Long,
) {
    var running: Boolean = running
        private set

    private var elapsedNanos: Long = elapsedSeconds * 1_000_000_000L

    fun elapsedSeconds(): Long = elapsedNanos / 1_000_000_000L

    fun tick(deltaNanos: Long) {
        if (running) elapsedNanos += deltaNanos
    }

    fun end() {
        running = false
    }
}
```

Minecraftのtick単位すら知らない、「経過ナノ秒を渡されたら足すだけ」の純粋な値オブジェクト。サーバー起動時、`port.ChallengeState`（`running`/`id`）と`port.RecordRepository`（`lastKnownElapsedTime`）から読み取った値で1個組み立てる。

## `StructuredValue`・`Trigger`・`RecordEvent`：自己記述する木構造

`Trigger`と`RecordEvent`はどちらも「保存対象のデータ」を持つ。これをファイルに書き出す形（現状はJSON）へどう変換するかには2つの選択肢がある。

| 選択肢                             | 内容                                                                                                                                                                                      | トレードオフ                                                                                                                                                                                                                                                                                                                                                                                                  |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| A: adapter層で集中変換             | domainのTriggerは何のメソッドも持たない純粋なマーカーinterfaceとし、変換は`adapter`層の`when`（Trigger種類ごとに分岐）または実行時レジストリ（`Map<KClass<out Trigger>, ...>`）に集約する | domainはシリアライズ形式を一切知らずに済む。ただし`when`+`else`方式は新しいTrigger実装を追加するたびに中央のファイルを直す必要があり、モジュール外からTriggerを拡張できるようにした意味が薄れる。レジストリ方式はその点を解決するが、「登録し忘れ」が実行時エラーになり、コンパイル時には検出できない                                                                                                         |
| B: Trigger自身が変換を持つ（採用） | `Trigger`（および`RecordEvent`）が構造を自分で語る。`StructuredValue`はJSON・Gsonに依存しない、domain所有の汎用木構造（`Map`や`List`と同格の概念）                                        | 新しい実装は`interface`を満たさない限りコンパイルが通らないため、「登録し忘れ」という状態が構造的に存在しない。adapter層に残る変換は`StructuredValue ⇔ Gson JsonElement`という1つの汎用関数だけになり、Trigger/RecordEventの種類ごとの分岐がadapterのどこにも無くなる。domainが持つのは「ネスト可能な汎用値」という抽象概念であり、Gson/JSONという具体的な表現形式への依存はadapterの`toGson()`側だけに閉じる |

Bをそのまま`fun describe(): StructuredValue.Obj`という1メソッドで実装すると、返す`Obj`に`"kind"`キーを含めるかどうかは各実装の裁量に委ねられ、**読み取り側（`RawTrigger`/`RecordEventView`）が前提にしている`"kind"`/`"type"`キーの存在をコンパイラは何も保証しない**（書き忘れても型エラーにならず、実行時に初めて壊れる）。そこで`Trigger`/`RecordEvent`は「タグ（`kind`/`type`）」と「タグ以外のデータ（`fields()`）」を別々のメンバーに分離し、`describe()`は両者を合成するだけの**オーバーライド不可な関数**にする。これにより`"kind"`/`"type"`キーの存在はinterfaceの契約（`val kind: String`の実装必須）として構造的に保証される。

```kotlin
// domain/StructuredValue.kt
sealed interface StructuredValue {
    data class Str(val value: String) : StructuredValue
    data class Num(val value: Long) : StructuredValue
    data class Bool(val value: Boolean) : StructuredValue
    data class Arr(val items: List<StructuredValue>) : StructuredValue
    data class Obj(val fields: Map<String, StructuredValue>) : StructuredValue

    companion object {
        fun of(value: String): StructuredValue = Str(value)
        fun of(value: Long): StructuredValue = Num(value)
        fun of(value: Boolean): StructuredValue = Bool(value)
        fun obj(vararg fields: Pair<String, StructuredValue>): Obj = Obj(fields.toMap())
    }
}
```

```kotlin
// domain/Trigger.kt
interface Trigger {
    val kind: String
    fun fields(): StructuredValue.Obj // kind以外のデータのみ。"kind"キーはここに含めない
}

// describe()はkindをoverrideし忘れない限りコンパイルが通らない。interface内でoverride不可な
// 拡張関数にすることで、"kind"キーを省略した実装が生まれる余地自体を無くす
fun Trigger.describe(): StructuredValue.Obj =
    StructuredValue.Obj(mapOf("kind" to StructuredValue.of(kind)) + fields().fields)

data class BossTrigger(val mobId: String) : Trigger {
    override val kind = "boss"
    override fun fields() = StructuredValue.obj("mobId" to StructuredValue.of(mobId))
}

data class ManualTrigger(val player: String) : Trigger {
    override val kind = "manual"
    override fun fields() = StructuredValue.obj("player" to StructuredValue.of(player))
}
```

`Trigger`をsealedにしない理由：`Trigger`を型で分岐する`when`がapplication層にもdomain層にも存在せず、常に多態的に扱われるだけなので、sealedの利点（網羅チェック）が空振りし、モジュール外からの拡張を塞ぐデメリットだけが残る。

新しいTrigger（アイテム入手等）を追加する側は、**完全新規のdomainファイル1つを追加するだけ**でよい。既存ファイルは一切触らない。

```kotlin
// domain/ItemObtainedTrigger.kt — 完全新規ファイル
data class ItemObtainedTrigger(val player: String, val itemId: String) : Trigger {
    override val kind = "item_obtained"
    override fun fields() = StructuredValue.obj(
        "player" to StructuredValue.of(player),
        "itemId" to StructuredValue.of(itemId),
    )
}
```

`RecordEvent`は`sealed`で閉じているためadapter層に`when`を置いても網羅チェックは効き、拡張性の問題自体は起きない。それでも同じ`describe()`パターンを採用するのは、**Trigger/RecordEventの間で変換方式を2種類使い分けるより、1種類に統一した方がシンプルで、adapterに種類ごとの変換コードが一切残らなくなる**ため。

```kotlin
// domain/RecordEvent.kt
sealed class RecordEvent(val elapsedTime: Long, val timestamp: String) {
    abstract val type: String
    abstract fun fields(): StructuredValue.Obj // type/elapsedTime/timestamp以外のデータのみ

    class Save(
        elapsedTime: Long,
        timestamp: String,
        val archiveName: String,
        val trigger: Trigger,
    ) : RecordEvent(elapsedTime, timestamp) {
        override val type = "save"
        override fun fields() = StructuredValue.obj(
            "archiveName" to StructuredValue.of(archiveName),
            "trigger" to trigger.describe(),
        )
    }

    class Clear(
        elapsedTime: Long,
        timestamp: String,
        val trigger: Trigger,
    ) : RecordEvent(elapsedTime, timestamp) {
        override val type = "clear"
        override fun fields() = StructuredValue.obj("trigger" to trigger.describe())
    }

    class Death(
        elapsedTime: Long,
        timestamp: String,
        val deadPlayer: PlayerRef,
        val killLog: String,
    ) : RecordEvent(elapsedTime, timestamp) {
        override val type = "death"
        override fun fields() = StructuredValue.obj(
            "deadPlayer" to StructuredValue.obj(
                "uuid" to StructuredValue.of(deadPlayer.uuid),
                "name" to StructuredValue.of(deadPlayer.name),
            ),
            "killLog" to StructuredValue.of(killLog),
        )
    }
}

// Triggerのdescribe()と同じ理由で、"type"/"elapsedTime"/"timestamp"の3キーは
// override不可な拡張関数で合成する。個々のRecordEventサブクラスはfields()だけを書けばよい
fun RecordEvent.describe(): StructuredValue.Obj = StructuredValue.Obj(
    mapOf(
        "type" to StructuredValue.of(type),
        "elapsedTime" to StructuredValue.of(elapsedTime),
        "timestamp" to StructuredValue.of(timestamp),
    ) + fields().fields
)
```

adapter層に残る変換はこれだけになる。`RecordService`（application層）が保存時に呼ぶのは`event.describe().toGson()`のみで、Trigger/RecordEventの種類ごとの分岐はadapterのどこにも存在しない。

```kotlin
// adapter/file/StructuredValueGson.kt — 新しいTrigger/RecordEventが増えても、このファイルは変更しない
fun StructuredValue.toGson(): JsonElement = when (this) {
    is StructuredValue.Str -> JsonPrimitive(value)
    is StructuredValue.Num -> JsonPrimitive(value)
    is StructuredValue.Bool -> JsonPrimitive(value)
    is StructuredValue.Arr -> JsonArray().apply { items.forEach { add(it.toGson()) } }
    is StructuredValue.Obj -> JsonObject().apply { fields.forEach { (k, v) -> add(k, v.toGson()) } }
}
```

## `application`層：判断ロジックを3つのServiceに分割する

将来「アイテム入手」「マルチブロック建造」等、チェックポイント/クリアのきっかけが増えることを見越し、`ChallengeApplicationService`の公開APIは**起きたことの種類（`onBossKilled`等）ではなく、結果として何を記録するか**で分ける。内部の実処理は3つのServiceに委譲し、Application Service自身は薄いオーケストレーションに徹する。

```kotlin
// application/ChallengeService.kt — Challengeの生成・進行・終了、ChallengeStateとの同期
class ChallengeService(
    private val challenge: Challenge,
    private val state: ChallengeState, // port
) {
    val id: String get() = challenge.id
    fun elapsedSeconds(): Long = challenge.elapsedSeconds()
    fun tick(deltaNanos: Long) = challenge.tick(deltaNanos)
    fun end() {
        challenge.end()
        state.running = false
    }
}

// application/ArchiveService.kt — アーカイブ名の採番とArchiveGatewayの呼び出し
class ArchiveService(private val gateway: ArchiveGateway) { // port
    fun archiveWithGeneratedName(elapsedSeconds: Long): String {
        val name = generateTimestampName()
        gateway.archive(name, elapsedSeconds)
        return name
    }
    fun archive(name: String, elapsedSeconds: Long): Boolean = gateway.archive(name, elapsedSeconds)
}

// application/RecordService.kt — RecordEventの組み立てとRecordRepositoryへの保存
class RecordService(private val repository: RecordRepository) { // port
    fun appendSave(challengeId: String, elapsedSeconds: Long, archiveName: String, trigger: Trigger) { ... }
    fun appendClear(challengeId: String, elapsedSeconds: Long, trigger: Trigger) { ... }
    fun appendDeath(challengeId: String, elapsedSeconds: Long, player: PlayerRef, killLog: String) { ... }
}

// application/ChallengeApplicationService.kt — 薄いオーケストレーションのみ
class ChallengeApplicationService(
    private val challengeService: ChallengeService,
    private val archiveService: ArchiveService,
    private val recordService: RecordService,
) {
    fun onServerTick(deltaNanos: Long) = challengeService.tick(deltaNanos)

    fun recordCheckpoint(trigger: Trigger) {
        val name = archiveService.archiveWithGeneratedName(challengeService.elapsedSeconds())
        recordService.appendSave(challengeService.id, challengeService.elapsedSeconds(), name, trigger)
    }

    fun recordClear(trigger: Trigger) {
        val name = archiveService.archiveWithGeneratedName(challengeService.elapsedSeconds())
        recordService.appendClear(challengeService.id, challengeService.elapsedSeconds(), trigger)
        challengeService.end()
    }

    fun onPlayerDeath(player: PlayerRef, killLog: String) {
        recordService.appendDeath(challengeService.id, challengeService.elapsedSeconds(), player, killLog)
        challengeService.end()
    }
}
```

新しいトリガー源（アイテム入手、マルチブロック等）は**adapter層への追加だけ**で完結し、`application`層は一切変更しない（オープン・クローズド原則）。

```kotlin
// 将来追加する例。既存のadapterファイルは一切触らない
@EventBusSubscriber(modid = HardcoreTogether.ID)
object ItemPickupListener {
    @SubscribeEvent
    fun onItemPickup(event: ItemEntityPickupEvent) {
        val itemId = ...
        val trigger = ItemObtainedTrigger(player.uuid, itemId)
        when {
            ItemTriggerConfig.isClearItem(itemId) -> applicationService.recordClear(trigger)
            ItemTriggerConfig.isCheckpointItem(itemId) -> applicationService.recordCheckpoint(trigger)
        }
    }
}
```

## `FileRecordRepository`：`MinecraftServer`ではなく`dir: Path`を取る

`RecordRepository`の実装が本当に必要としているのは「レコードを置くディレクトリ」だけであり、`MinecraftServer`を引数に取る選択肢はMinecraft/NeoForgeへの不要な依存を持ち込む。`dir: Path`を取る形を採用し、`server.serverDirectory.resolve("records")`の解決は呼び出し側（`adapter/neoforge`）が1回だけ行う。

```kotlin
class FileRecordRepository(private val dir: Path) : RecordRepository
```

これによりMinecraft/NeoForgeの型に一切依存しない純粋なファイルI/Oクラスになり、単体テストが書きやすくなる。

## デシリアライズ（読み取り）はhardcoretogetherの範囲外

以前はここに`RecordEventView`/`RawTrigger`（読み取り専用のview model）と、`/savedata`・`/senpan`・タイムライン表示（`EventNarrator`）のための`JSON→domain`変換を置いていた。

**【設計変更】** `specification.md` 2.6節の通り、`/savedata`・`/senpan`はhardcore MODを経由せず、Gate（同一ホスト前提、Go実装）が`records/<challengeId>.json`を直接読み取って実現する設計に変わった。理由は2つ：①これらのコマンドを「hardcoreに接続していなくてもどこからでも実行できる」ようにするため、②hardcoreサーバーが停止中でも過去の記録を閲覧できるようにするため（MODが生きていないと読めない設計では停止中に使えない）。

この結果、`hardcoretogether`（Kotlin/NeoForge側）は**書き込み専用**になり、上記の読み取り側の型・変換・タイムライン整形ロジックはすべて不要になった：

- `port/RecordEventView.kt`（`RawTrigger`・`RecordEventView`・`ChallengeRecord`）→ 削除
- `RecordRepository.listAll()` / `FileRecordRepository.listAll()` → 削除（`RecordRepository`は`appendEvent`・`updateLastKnownElapsedTime`・`lastKnownElapsedTime`の3メソッドのみ）
- `adapter/file/StructuredValueGson.kt`の読み取り方向（`toStructuredValue()`・`toRecordEventView()`・`toRawTrigger()`）→ 削除。`StructuredValue.toGson()`（書き込み方向）のみ残る
- `adapter/neoforge/EventNarrator.kt`（構想段階、未実装） → Gate側（Go実装）の責務になったため実装不要
- `domain/Challenge.kt`の`formatElapsed()` → 呼び出し元（`/savedata`表示）が無くなったため削除

「クラス名を保存しリフレクションで復元」ではなく「`kind`文字列＋汎用フィールド」という読み取りモデルの設計判断自体は無駄にならない：Gate（Go実装）が`records/*.json`を読む際も、`kind`フィールドが将来のリファクタリング・MOD削除に対して安定した識別子であるという性質はそのまま活きる。単にその読み取りロジックの実装言語・実行プロセスがhardcoretogether（Kotlin）からGate（Go）に移っただけである。

## `/archive`アーカイブ経路の非同期化

### 問題

`ArchiveGatewayImpl.archive()`は`TcpGateConnection.sendArchiveRequestAndAwait()`を介して`archive-complete`/`archive-rejected`（`requestId`相関、`protocol-mod-manager.md` 3.3〜3.5節）を受信するまで同期的にブロックしていた。この呼び出しは`CommandRegistrar.kt`の`/archive`（Brigadierコマンドディスパッチ）と`DeathCountdown.kt`の`handleBossKill()`（`LivingDeathEvent`ハンドラ）の2箇所から行われ、**どちらもサーバーのメインスレッド上**で発生する。ブロック中はTPSが停止し他コマンドも処理されない実害が`/archive`側で実機で見つかっている（`specification.md` 3.2節「`archive-rejected`の追加経緯」）。`handleBossKill()`側は同種のバグ報告こそ無かったが、同じブロッキング呼び出しを経由している以上、構造的には同じ問題を抱えていた。

### 方針：send-and-forget化＋コールバック

`TcpGateConnection`を「送って`future.get()`で待つ」実装から、「送って、応答（`archive-complete`/`archive-rejected`、`requestId`相関）を受信したreaderスレッドが登録済みのコールバックを直接呼ぶ」実装に変えた。タイムアウトも`future.get(timeout)`ではなく、送信時に別途スケジュールし、応答到着とタイムアウト発火のどちらが先でも二重発火しないようにしている（既存の「`requestId`をキーに一度だけ取り出して処理する」パターンをそのまま踏襲）。

**「デッドロックの教訓」（下記「設計判断まとめ」参照）の遵守**：readerスレッドはコールバックを直接呼ぶが、コールバック自体はメインスレッドへ処理を委譲するだけで即座に返り、readerスレッド上で何かを待つことは一切しない。

### 未接続時はタイムアウトを待たず即座に失敗させる

Gateへ一度も接続できていない、または接続済み後にreaderスレッドが切断を検知した状態（`TcpGateConnection`が接続用ソケット・書き込み用ストリームを持っていない状態）で`archive-request`を送ろうとした場合、実際にはメッセージは送信されずログに警告を出して捨てられるだけになる（既存の送信処理の挙動）。ところがこれまでの実装は、送信できたかどうかを一切見ずに無条件で応答を待つため、**応答が絶対に届かないと分かっている状況でも、律儀に60秒間待ってからタイムアウト扱いにする**。同期実装ではこれがそのままメインスレッドの60秒フリーズになり、非同期化後も、後述のFIFOキューが「直前のリクエストの応答を待ってから次へ進む」直列化をしている都合上、**未接続の間はキューに溜まったリクエスト1件ごとに60秒ずつ浪費してから次に進む**という形で問題が残る。

これを避けるため、送信時点で未接続と分かっている場合は、実際の送信もタイムアウトのスケジュールも行わず、その場（呼び出し元と同じくメインスレッド上）で即座に失敗として扱う。応答を実際に待った上でのタイムアウトと、そもそも送っていない未接続とはOPへ伝えるべき内容が異なる（前者は「Manager側が処理に手間取っている可能性がある」、後者は「Gate接続そのものが切れている」）ため、`ArchiveResult`に両者を区別する新しいケース（`NotConnected`）を追加し、`CommandRegistrar`はこれを受けて「Gateに接続されていません」といった趣旨のメッセージを表示する。接続状態の判定は`TcpGateConnection`が既に持っているソケット・書き込みストリームの有無で足り、新しい状態変数の追加は不要だった。

この対応により、同期実装で既に起きている「未接続時に60秒フリーズする」問題自体も解消される（非同期化を待たずに直せる部分だが、非同期化と合わせて設計・実装するのが自然なため、ここにまとめて設計した）。なお`ready`・`running-changed`は応答を伴わない一方向の通知であり、そもそも待ち合わせが発生しないため、この設計の対象外である。

### 接続断時：保留中リクエストを即座に解決してからタイムアウト検出用スレッドを止める

`TcpGateConnection`には稼働中に接続が切れた場合の再接続ロジックが無い（起動時の`connect()`が数回リトライして諦めた後は、そのサーバーセッションの間ずっと未接続のままで、再接続を試みる仕組みはどこにも無い）。したがって、readerスレッドが切断を検知した時点（EOF/`IOException`により`socket`/`writer`を`null`へ戻す箇所）で、**その接続はそのセッションにおいてもう二度と使われない**ことが確定する。fast-fail設計により以後`archive()`が呼ばれても新規のタイムアウトタスクは積まれなくなるため、タイムアウト検出用スレッドもこの時点で存在理由を失う——スレッドの生存期間を「サーバープロセス」ではなく「この接続」に結びつけてよい。

ただし、切断検知時点で**まだ応答待ちの`archive-request`が残っている可能性**がある（接続が生きている間に送信済みで、`archive-complete`/`archive-rejected`もタイムアウトもまだ来ていないもの）。これらのコールバックが呼ばれる手段は、本物の応答（readerスレッドは既に死んでいるので二度と来ない）か、スケジュール済みのタイムアウトタスクの発火のいずれかしか無い。ここでタイムアウト検出用スレッドをいきなり止めてスケジュール済みタスクごと切り捨てると、これらのコールバックは永久に呼ばれなくなる。`ArchiveGatewayImpl`のFIFOキューは「コールバックが呼ばれたら次のリクエストへ進む」設計のため、これが起きるとキューが動かなくなったまま止まってしまう——スレッドが残り続けるより深刻な、キューの停止を引き起こす。

そのため切断検知時には、①保留中の全リクエストを取り出し、それぞれのコールバックへ即座に「未接続」相当の結果（前述の`ArchiveResult`の未接続ケースを流用する）を渡して解決してから、②タイムアウト検出用スレッドを止める、という順序で処理する。これにより、切断が起きた瞬間に待たされていたコマンド・ボスキル側もタイムアウトを待たずに即座に失敗通知を受け取れる。「送信時点で未接続なら即失敗」（前節）と対になる、「送信後に接続が切れたら即失敗」という設計であり、考え方に一貫性がある。

### `ArchiveGatewayImpl`：flushもリクエスト間で直列化する

非同期化により、手動`/archive`の応答待ち中に自動アーカイブ（ボスキル）が割り込む、といった**複数の`archive-request`が真に並行してin-flightになる状況**が初めて現実に起こり得るようになる（同期実装ではメインスレッドが1件目の`archive-complete`待ちでブロックされている間、2件目のトリガー自体が発生しようがなかったため、これまでは構造的に起こり得なかった）。この状態で後発リクエストの`archive()`呼び出しが無条件に`save-all flush`を発行すると、**Manager側がまだ先発リクエストのワールドフォルダコピーを実行中である可能性がある**——OSレベルのファイルコピーは原子的ではないため、その最中に後発のflushでディスク上のファイルが書き換わると、先発のアーカイブが新旧混在の歪んだ状態になる。これはsave-off/save-onブラケットが本来防ぐはずだった不具合（3.2節）を、オートセーブではなく明示的な`save-all flush`という別経路から引き起こしてしまう抜けである。

そのため`save-off`だけでなく`save-all flush`自体もリクエスト間で直列化する：`ArchiveGatewayImpl`に未処理リクエストのFIFOキューを持たせ、**次のリクエストのflushは、直前のリクエストの`archive-complete`/`archive-rejected`を受信し終えるまで発行しない**（同時にManagerへ送信済み・応答待ちなのは常に高々1件）。`save-off`はキューが空→非空になった時点の1回だけ、`save-on`はキューが空に戻った時点の1回だけ発行する。この直列化により、複数件溜まっている間は後発リクエストの応答が先発より遅れる（レイテンシの犠牲）が、`archive()`自体はキューに積むだけで即座に返るためメインスレッドは一切ブロックせず、非同期化の目的（TPS停止の解消）は損なわれない。

キューの状態（未処理リクエスト・save-off中かどうか・現在in-flightかどうか）は`AtomicInteger`等の同期プリミティブを使わないただのフィールドでよい：書き換えが起きるのは①`archive()`呼び出し時（呼び出し元は`CommandRegistrar`・`DeathCountdown`のいずれもメインスレッド）と②応答コールバック内（メインスレッドへ処理を委譲した後）の2箇所のみで、どちらも必ずメインスレッド上でしか実行されない（readerスレッドはコールバックを起動するだけでこれらの状態には一切触れない）ため。

### `ArchiveService`／`ChallengeApplicationService`：コールバックの伝播

`archiveWithGeneratedName`・`archive`はいずれも結果を受け取るコールバックを追加で受け取り、`ArchiveGateway`へそのまま渡す形に変える。アーカイブ名の採番はMOD側で完結しているため、非同期化後も呼び出し側へ同期的に返せる。`recordCheckpoint`/`recordClear`が行う記録（`appendSave`/`appendClear`）と`recordClear`の`endChallenge()`（`running-changed: false`送信を含む）は、アーカイブ本体の完了を待たずに従来通りその場で実行する。

**順序に関する検討事項**：これにより、`running-changed: false`が`archive-complete`/`archive-rejected`より先にManagerへ届く順序が生じ得る（従来の同期実装では、アーカイブ完了＝`endChallenge()`実行だったため必ずアーカイブ完了後だった）。問題ないと判断した理由：プロトコル上`running-changed`と`archive-request`系シグナルの間に順序保証は無く、Manager側もこの2つを独立に処理する（`running`の永続化と`archive/`へのコピーは別々の関心事）。`specification.md`・`protocol-mod-manager.md`に順序依存の記述は無いため、この変更で壊れる契約は無い。

### `DeathCountdown`／`CommandRegistrar`：呼び出し側の変更

`DeathCountdown.handleBossKill()`は結果コールバックの中でログ出力するだけに変わる（従来通り記録は結果によらず必ず行う）。

`CommandRegistrar`の`/archive`は、コマンド実行直後に「アーカイブ処理を開始しました」という受理メッセージを即座に返し、実際の成否（`保存完了`／拒否理由／タイムアウト／未接続）は結果コールバックが呼ばれた時点で別途OPへ通知する形に変える。コールバックはコマンド実行から数tick遅れて呼ばれるため、`CommandSourceStack`をその間保持することになるが、`sendSuccess`/`sendFailure`はその時点で送信先が有効である限り安全に呼べる（既存コードも送信者が`ServerPlayer`かどうかをnull許容に扱っている）。**手動`/archive`の記録タイミングは変更しない**：`recordService.appendSave`は従来通りアーカイブ成功時のみ呼ぶ（自動アーカイブ側の「記録は結果によらず必ず行う」という非対称な既存仕様とは意図的に揃えていない、`application`層の項参照）。

手動`/archive`実行中に自動アーカイブが割り込む、といったケースを`/archive`コマンド自体が拒否する必要は無い：`ArchiveGatewayImpl`のキューが自動的に後発リクエストを積んで直列化するため、MOD側で追加の排他制御を持つ理由が無い（`requestId`は各リクエストの応答相関のために引き続き必要）。Manager側は`opMutex`を別途持つ（`architecture-manager.md`参照）が、これはMOD側のキューと独立な、Manager自身の内部状態を守るための排他制御である。

## 設計判断まとめ

- レイヤー構成：domain / application / port / adapterの4層。判断ロジックを1クラスに集約せず、`ChallengeService`/`ArchiveService`/`RecordService`＋薄い`ChallengeApplicationService`に分割。真のdomain（Challenge/Trigger/RecordEvent/BossCategory）はportに一切依存しない
- `RecordRepository`の実装は`MinecraftServer`ではなく`dir: Path`を受け取る（Minecraft非依存でテストしやすくするため）
- `RecordEvent`は1つのnullableフィールドdata classではなく、sealed class（Save/Death/Clear）にする
- `Challenge`をdomainエンティティとして持つ。idと経過時間（経過ナノ秒）をそのまま持つ、tick単位すら知らない純粋な値オブジェクト
- `GateClient`・`ElapsedTimeTracker`・`DeathHandler`（ミュータブル状態を持つobject）はインスタンス化されたクラスに置き換える。`HardcoreTogether`はNeoForgeのイベント検出のためobjectのまま残すが、実体は各クラスへの薄い委譲のみ行う
- `Trigger`・`RecordEvent`は自分の構造を自分で語る。ただし`describe(): StructuredValue.Obj`を素朴に1メソッドでoverrideさせる形だと、`"kind"`/`"type"`キーを書き忘れても型エラーにならない。そこで`Trigger`は`val kind: String`＋`fun fields()`、`RecordEvent`は`val type: String`＋`fun fields()`に分離し、`describe()`は両者を合成するoverride不可な拡張関数にすることで、`"kind"`/`"type"`キーの存在をinterfaceの契約として構造的に保証する。`StructuredValue`（Str/Num/Bool/Arr/Obj）はJSON非依存の汎用木構造としてdomainに置く。adapter層に残るのは`StructuredValue ⇔ Gson JsonElement`の汎用変換1つのみで、Trigger/RecordEventの種類ごとの分岐はadapterのどこにも存在しない
- 「溶岩遊泳を試みた」のようなフレーバーのある表現は、既存の`RecordEvent.Death.killLog`で賄う。save/death/clear以外の新しい`RecordEvent`種類は現時点では追加しない
- 【設計変更】`/savedata`・`/senpan`はGate（同一ホスト前提、Go実装）が`records/*.json`を直接読み取る形にし、hardcore MODから読み取り側（`RecordEventView`/`RawTrigger`/`ChallengeRecord`/`EventNarrator`/`StructuredValueGson`の読み取り方向）を全て削除した。理由：これらのコマンドをhardcore接続中・稼働中に限定せず「どこからでも」実行できるようにするため（specification.md 2.6節）
- 【検討の上、不採用】`/archive`もGateレベルのコマンドに統合し、Gateがhardcore MODへ新設シグナルを送って中継する案を一時採用したが、撤回した。理由：`/archive`は稼働中のMinecraftサーバープロセスでしか実行できない操作（オートセーブの一時停止）を必ず伴うため「hardcoreに接続していなくても実行できる」利点が実質的に無く、中継のために必要になる複雑さ（新規シグナル2種、`ArchiveGateway.onArchiveCommand`、Gate接続のreaderスレッドで直接処理するとブロッキングによりデッドロックする問題への対処として`server.execute{}`でメインスレッドへ処理を退避させる仕組み、hardcoreに繋いでいない実行者へのGateからのメッセージ配送）に見合わなかった。`/archive`は`adapter/neoforge/CommandRegistrar.kt`が直接登録する、従来通りの設計に戻した
  - **デッドロックの教訓は残しておく価値がある**：Gate接続のreaderスレッドで受信した何かを処理する際、その処理が同じ接続の別メッセージ（`archive-complete`等）を待ってブロックする可能性があるなら、readerスレッド自身の上で直接処理してはいけない（readerスレッドがブロックされ、待っているメッセージ自体を二度と読めなくなる）。今後同様の「Gateからの着信を受けて何か重い処理をする」機能を追加する際は、必ずこの制約を思い出すこと
- `records/`の配置パスを設定可能にした。`records/<challengeId>.json`はhardcore MOD（書き込み）とGate（読み取り、`/savedata`・`/senpan`）という別プロセスがまたいで読む唯一のデータであり、パスをハードコードすると2つの設定が食い違うリスクがあるため、hardcore MOD側の`ModConfigSpec`（`storage.recordsDir`）から指定できるようにした（デフォルト値は従来通り`records`）。旧`BossConfig`をボス設定専用のまま残さず、`HardcoreConfig`という名前でストレージ設定も持たせる形にリネームした：NeoForgeはMOD1つにつきCOMMON設定ファイル1つしか持てないため、2つ目の`ModConfigSpec`を新設する選択肢は無く、既存の唯一のspecに統合する必要があった（`ModConfig`という名前はNeoForge自身の型と衝突するため使えない）

## 実装済み

上記構成は`hardcoretogether/src/main/kotlin/com/ray/light/hardcoretogether/`に実装済み（`compileKotlin`で検証済み）。`port.ChallengeState`・`RecordService`/`ArchiveService`のシグネチャ、`HardcoreTogether`/`Runtime`での配線コードも確定・実装済み。`/archive`は`CommandRegistrar.kt`がhardcore MODの直接コマンドとして登録し、`/savedata`・`/senpan`はhardcore MOD側には一切存在しない（Gate側の実装に完全移譲）。「`/archive`アーカイブ経路の非同期化」節の設計（`TcpGateConnection`のsend-and-forget化、未接続時のfast-fail、接続断時のタイムアウト検出用スレッドの後始末、`ArchiveGatewayImpl`のFIFOキューによるflush直列化）も実装済み。ワイヤプロトコル自体に変更は無いMOD内部実装のみの変更のため、Manager（Go）側の対応は不要。
