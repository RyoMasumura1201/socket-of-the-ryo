package main

// ============================================================
// ここが今日の主役。
// これまで「カーネルの中の何か」としか言えなかった
//   ・ソケット
//   ・コネクション
// を、自分で読めるGoのstructとして定義する。
// これで fmt.Println でも中身を覗けるようになる。
// ============================================================

// State はコネクションの状態。TCPの状態遷移図そのもの(主要なものだけ抜粋)。
type State int

const (
	StateClosed      State = iota // 何もない
	StateListen                   // 待ち受け中(窓口)
	StateSynSent                  // SYNを送ってSYN+ACK待ち(接続を仕掛けた側)
	StateSynReceived              // SYNを受けてSYN+ACKを返し、最後のACK待ち
	StateEstablished              // 確立!データをやり取りできる
	StateClosing                  // 終了処理中(このミニ実装では簡易)
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateListen:
		return "LISTEN"
	case StateSynSent:
		return "SYN_SENT"
	case StateSynReceived:
		return "SYN_RCVD"
	case StateEstablished:
		return "ESTABLISHED"
	case StateClosing:
		return "CLOSING"
	}
	return "?"
}

// TCB = Transmission Control Block = 「コネクションそのもの」の実体。
// これがOS(カーネル)の中で1コネクションにつき1個持っている状態。
// 本物のカーネルにも literally この名前の構造体がある。
//
// 注目: コネクションとは「状態(state)」と「番号(sndNxt/rcvNxt)」を
//       持ったただのデータ。電話線のような物理的存在ではなく、
//       両端が同じルールで番号を管理し合っている“約束事”にすぎない。
type TCB struct {
	state State

	sndNxt uint32 // 次に自分が送るバイトのseq番号(Send NeXT)
	rcvNxt uint32 // 次に相手から受け取るはずのseq番号(ReCeiVe NeXT)

	// --- アプリ(ユーザーのgoroutine)と橋渡しするためのchannel ---
	acceptCh    chan *Socket  // 待ち受けソケット用: 確立した接続を積むキュー
	recvCh      chan []byte   // 受信データをアプリに渡す
	establishCh chan struct{} // 接続確立をConnect()に知らせる
}

// Socket = 「コネクションを操作するための取っ手(fd)」に相当。
// アプリはこの Socket を握って Accept/Send/Recv/Close を呼ぶ。
//
// 待ち受けソケットも通信ソケットも、同じこの型で表す。
// 違いは tcb.state が LISTEN か ESTABLISHED か、という中身だけ。
type Socket struct {
	host *Host // どのホスト(OS)に属するか

	// この4つ組がコネクションを一意に決める(01-echoで学んだやつ)。
	// 待ち受けソケットは remoteHost/remotePort が空(相手未定)。
	localPort  int
	remoteHost string
	remotePort int

	tcb *TCB // このソケットが指すコネクションの実体
}

// ---- アプリから見えるAPI(syscall相当)----

// Accept は待ち受けソケットで、確立済みコネクションを1つ取り出す。
// 01-echo の ln.Accept() と同じ。キューが空なら届くまでブロックする。
func (s *Socket) Accept() *Socket {
	conn := <-s.tcb.acceptCh // 確立済みコネクションが積まれるまで待つ
	return conn
}

// Send はこのコネクションでデータを送る。
func (s *Socket) Send(data []byte) {
	s.host.appSend(s, data)
}

// Recv はこのコネクションで届いたデータを1つ受け取る。
// 何も来ていなければ届くまでブロックする。okがfalseなら相手が閉じた(EOF)。
func (s *Socket) Recv() (data []byte, ok bool) {
	data, ok = <-s.tcb.recvCh
	return
}

// Close はこのコネクションを閉じる(簡易版: FINを送るだけ)。
func (s *Socket) Close() {
	s.host.appClose(s)
}
