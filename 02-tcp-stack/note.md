# 02-tcp-stack 学習ノート: GoでTCPスタックを自作する

「ソケットとコネクションが結局カーネルの中の何かとしか説明されず、ピンとこない」
→ ならその“カーネルの中”を自分のGoのコードで書いてしまえばいい、という回。
純Goのシミュレーションで、配線・カーネル・ソケット・コネクションを全部 struct にした。

`01-echo` でOSに丸投げしていた `accept()` の裏側を、今回は自分で実装した関係。

## このディレクトリの中身

| ファイル | 対応する概念 | 内容 |
|---|---|---|
| `packet.go` | パケット | 配線を流れる1単位。フラグ(SYN/ACK/FIN/PSH)とseq/ackとPayloadだけ |
| `network.go` | 配線(ケーブル/ルーター) | パケットを宛先ホストのinboxへ運ぶだけの配送係 |
| `host.go` | OS / ネットワークスタック | 受信パケットを処理しTCPの状態遷移を進める本体。Listen/Connect/handle |
| `socket.go` | ソケット & コネクション | `Socket`(取っ手=fd相当)と `TCB`(コネクション実体)の定義 |
| `main.go` | アプリ | サーバーとクライアントを動かしEchoさせるデモ |

実行:
```bash
go run ./02-tcp-stack
```

---

## 登場人物の対応関係

| この実装 | 本物 | 役割 |
|---|---|---|
| `Network` | ケーブル/ルーター/NIC | パケットを運ぶだけ。中身に興味なし |
| `Host` | OS(カーネル)1台 | ネットワークスタック本体。ソケット表を持つ |
| `Host.inbox` (channel) | NICの受信バッファ | 届いたパケットが積まれる |
| `Host.run()` | カーネルの割り込み処理ループ | inboxを処理し状態遷移を進める |
| `Socket` | ソケット(fd) | コネクションを操作する取っ手 |
| `TCB` | Transmission Control Block | コネクションそのもの(state+seq/ack) |
| `Listen/Connect/Accept/Send/Recv` | syscall | アプリ↔カーネルの境界 |

---

## いちばんの学び: ソケットとコネクションの正体

### コネクション = TCB という「データ」
```go
type TCB struct {
    state  State   // CLOSED/LISTEN/SYN_SENT/SYN_RCVD/ESTABLISHED...
    sndNxt uint32  // 次に自分が送るseq番号
    rcvNxt uint32  // 次に相手から受け取るはずのseq番号
}
```
- コネクションは電話線のような物理的存在ではない。
- **両端のホストが、同じルールで seq/ack を管理し合っている「約束事」**にすぎない。
- 配線(`Network`)はコネクションのことを一切知らない。パケットを運ぶだけ。
- コネクションは**両端の TCB の中だけ**に存在する。

### ソケット = TCB を操作する「取っ手(fd)」
```go
type Socket struct {
    localPort  int
    remoteHost string   // 待ち受けソケットは空(相手未定)
    remotePort int
    tcb        *TCB
}
```
- 待ち受けソケットも通信ソケットも同じ `Socket` 型。
- 違いは `tcb.state` が LISTEN か ESTABLISHED か、という**中身だけ**。

---

## 3ウェイハンドシェイクを状態遷移として実装した

実行ログ(抜粋):
```
[client] CLOSED   -> SYN_SENT    : send [SYN]     seq=2000
[server] LISTEN   -> SYN_RCVD    : send [SYN,ACK] seq=3000 ack=2001
[client] SYN_SENT -> ESTABLISHED : send [ACK]     ack=3001
[server] SYN_RCVD -> ESTABLISHED : accept待ちキューへ投入
```

ポイント:
- `seq=2000` に対し `ack=2001`(+1して返す)= 「ここまで受け取った」の意味。
- サーバーが最後のACKを受けて `SYN_RCVD -> ESTABLISHED` になった**瞬間**に、
  確立済みコネクションを `acceptCh` に積む(`host.go` の `ln.tcb.acceptCh <- s`)。
  → これが「**accept はキューから確立済みコネクションを取り出すだけ**」の正体。
  01-echo の note 5.5「キューにあるのはコネクション、acceptが取っ手を付けて返す」を
  実際にコードで再現したことになる。

### seq/ack はバイト数ぶん進む
最終状態:
```
TCB{state=ESTABLISHED sndNxt=2010 rcvNxt=3016}
```
- `sndNxt=2010` = SYN(1) + "hello tcp"(9バイト) = 2000+10
- `rcvNxt=3016` = SYN(1) + "echo: hello tcp"(15バイト) = 3000+16
- SYNとFINも「1バイト分」seqを消費する、という細かいルールもコードに反映済み。

---

## 01-echo との対応(どこを自分で書いたか)

```
01-echo/raw  : accept() を呼ぶ      … ハンドシェイクはOS(カーネル)任せ
02-tcp-stack : そのカーネルを自作   … SYN/SYN-ACK/ACK の状態遷移を自分で書いた
```

`accept()` の戻り値が「新しい通信ソケット」だったのは、
カーネルが SYN_RCVD のコネクションを ESTABLISHED にして acceptキューに積み、
それを取り出して fd を付けて返していたから — を実装で確認した。

---

## 結論(ひとことで)

- **コネクション** = 両端の TCB(state + seq/ack)が保つ約束事。物理的実体ではない。
- **ソケット** = その TCB を操作する取っ手(fd)。窓口用と通信用がある。
- **配線** はパケットを運ぶだけで、コネクションを知らない。
- **3ウェイハンドシェイク** = TCBの状態を CLOSED→...→ESTABLISHED へ遷移させる手続き。
- これらは全部、特別なハードウェアではなく**ただのデータと手続き**で出来ている。

## 育てるなら(次の課題)
- [ ] `net.trace = true` で配線を流れる生パケットも表示してみる
- [ ] 複数クライアントを同時接続させ、connKeyで正しく振り分くのを確認
- [ ] `Close` のFINハンドシェイクを完全実装(FIN/ACK/FIN/ACK + TIME_WAIT)
- [ ] パケットロスを配線でランダムに起こし、再送(retransmission)を実装
- [ ] 受信ウィンドウ(フロー制御)を入れてみる
- [ ] 発展: 純Go模擬ではなく、本物のTUN(utun)デバイスで生IPパケットを扱う版
