package main

import "fmt"

// Packet は「配線(ネットワーク)を流れる1個のパケット」。
// 本物のTCP/IPでは IPヘッダ + TCP�ッダ + データ だが、
// 学習用に必要な項目だけに絞っている。
//
// 注目: ここには「文字列」も「メッセージ」も無く、
//       制御フラグ・番号・バイト列(Payload)があるだけ。
//       コネクションは、この小さなパケットの往復だけで組み立てられる。
type Packet struct {
	SrcHost string // 送信元ホスト(IPアドレス相当)
	DstHost string // 宛先ホスト
	SrcPort int    // 送信元ポート
	DstPort int    // 宛先ポート

	// --- TCPの制御フラグ ---
	SYN bool // 接続を開始したい(seq番号の同期)
	ACK bool // ここまで受け取った、という確認応答
	FIN bool // 接続を閉じたい
	PSH bool // データを乗せている(push)

	// --- TCPの番号(これがコネクションの「状態」の核心) ---
	Seq uint32 // このパケットの先頭バイトの通し番号
	Ack uint32 // 「次はこの番号から欲しい」= ここまで受領済み

	Payload []byte // 実際に運ぶデータ(echoの中身など)
}

// flagsString は立っているフラグを [SYN,ACK] のように表示する(ログ用)。
func (p Packet) flagsString() string {
	s := ""
	add := func(name string, on bool) {
		if on {
			if s != "" {
				s += ","
			}
			s += name
		}
	}
	add("SYN", p.SYN)
	add("ACK", p.ACK)
	add("FIN", p.FIN)
	add("PSH", p.PSH)
	if s == "" {
		s = "-"
	}
	return "[" + s + "]"
}

// String はパケット1個を1行で読めるようにする(ログ用)。
func (p Packet) String() string {
	body := ""
	if len(p.Payload) > 0 {
		body = fmt.Sprintf(" data=%q", string(p.Payload))
	}
	return fmt.Sprintf("%s:%d -> %s:%d %s seq=%d ack=%d%s",
		p.SrcHost, p.SrcPort, p.DstHost, p.DstPort,
		p.flagsString(), p.Seq, p.Ack, body)
}
