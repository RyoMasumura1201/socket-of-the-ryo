// 02-tcp-stack: GoでTCPスタックを自作する(純Goシミュレーション)
//
// net パッケージもOSのソケットも使わない。
// 「配線」も「カーネル」も「ソケット」も「コネクション」も、全部自分のGoのstruct。
// これにより、これまで“カーネルの中の何か”だったものが目に見えるようになる。
//
//   Network … 配線(channelでパケットを運ぶだけ)
//   Host    … 自作OS(ネットワークスタック本体)。1台ごとにstackが動く
//   Socket  … コネクションを操作する取っ手(fd相当)
//   TCB     … コネクションそのもの(state と seq/ack を持つデータ)
//
// 実行:
//   go run ./02-tcp-stack
//
// 出力されるログを上から読むと、3ウェイハンドシェイクの状態遷移
// (SYN -> SYN+ACK -> ACK)が1行ずつ流れるのが見える。
package main

import (
	"log"
	"sync"
)

func main() {
	log.SetFlags(0) // 時刻表示を消して読みやすく

	// 配線を1本用意し、2台のホスト(自作OS)をつなぐ。
	net := NewNetwork()
	server := net.NewHost("10.0.0.1") // サーバー役
	client := net.NewHost("10.0.0.2") // クライアント役

	log.Println("==== セットアップ: server=10.0.0.1, client=10.0.0.2 ====")

	var wg sync.WaitGroup

	// ---- サーバー側 ----
	// 01-echoと同じ構造: Listen して Accept、来たデータをechoで返す。
	ln := server.Listen(80)
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn := ln.Accept() // ハンドシェイク完了まで待つ
		log.Println(">>> server: Accept完了。クライアントとのコネクション確立")

		data, ok := conn.Recv()
		if !ok {
			return
		}
		log.Printf(">>> server: 受信 %q。echoで返す", string(data))
		conn.Send(append([]byte("echo: "), data...))
	}()

	log.Println("==== クライアントが接続を開始 (3ウェイハンドシェイク) ====")

	// ---- クライアント側 ----
	conn := client.Connect("10.0.0.1", 80) // SYN -> SYN+ACK -> ACK で確立するまでブロック
	log.Println(">>> client: Connect完了。コネクション確立")

	log.Println("==== データ送受信 ====")
	conn.Send([]byte("hello tcp"))

	reply, ok := conn.Recv()
	if ok {
		log.Printf(">>> client: サーバーからの応答 = %q", string(reply))
	}

	wg.Wait()

	log.Println("==== 最終状態 ====")
	// コネクション(TCB)の中身を覗く。これが“見える化”の総仕上げ。
	log.Printf("client側ソケット: localPort=%d 相手=%s:%d  TCB{state=%s sndNxt=%d rcvNxt=%d}",
		conn.localPort, conn.remoteHost, conn.remotePort,
		conn.tcb.state, conn.tcb.sndNxt, conn.tcb.rcvNxt)
}
