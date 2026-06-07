package main

import (
	"log"
	"sync"
	"sync/atomic"
)

// Host = 自作OS(カーネル)1台分。中に「ネットワークスタック」を持つ。
// 役割:
//   - inbox に届いたパケットを処理し、TCPの状態遷移を進める(= スタック本体)
//   - listeners / conns でソケットを管理する(= カーネル内のソケット表)
type Host struct {
	addr string   // このホストのアドレス(IP相当)
	net  *Network // つながっている配線

	inbox chan Packet // 配線から届くパケットの受信箱(NIC受信バッファ相当)

	mu        sync.Mutex          // 下のマップを守るロック
	listeners map[int]*Socket     // ポート -> 待ち受けソケット
	conns     map[connKey]*Socket // 4つ組 -> 通信ソケット
	nextPort  int                 // クライアント側の一時ポート割り当て用
}

// connKey は「相手＋自分のポート」でコネクションを一意に識別する鍵。
// 同じ:80でも相手が違えば別コネクション、を表現する(01-echoの4つ組に対応)。
type connKey struct {
	localPort  int
	remoteHost string
	remotePort int
}

// isnCounter は初期シーケンス番号(ISN)を配る。
// 本物はランダムだが、学習用に分かりやすく増やしていく。
var isnCounter uint32 = 1000

func nextISN() uint32 {
	return atomic.AddUint32(&isnCounter, 1000)
}

// run はこのホストのネットワークスタックの心臓部。
// 受信箱に届いたパケットを1つずつ処理する(これが“OSが裏で動かす”ループ)。
func (h *Host) run() {
	for p := range h.inbox {
		h.handle(p)
	}
}

// trace は状態遷移を1行で見せるためのログ。今日の学びの可視化はここ。
func (h *Host) trace(format string, args ...any) {
	log.Printf("[%s] "+format, append([]any{h.addr}, args...)...)
}

// ============================================================
//   syscall 相当のAPI(アプリのgoroutineから呼ばれる)
// ============================================================

// Listen は net.Listen 相当。待ち受けソケットを作って登録する。
func (h *Host) Listen(port int) *Socket {
	h.mu.Lock()
	defer h.mu.Unlock()

	s := &Socket{
		host:      h,
		localPort: port,
		tcb: &TCB{
			state:    StateListen,
			acceptCh: make(chan *Socket, 128), // accept待ちキュー
		},
	}
	h.listeners[port] = s
	h.trace("Listen(:%d) -> 待ち受けソケット作成, state=%s", port, s.tcb.state)
	return s
}

// Connect は接続を仕掛ける側(クライアント)。
// SYNを送り、ハンドシェイクが終わって ESTABLISHED になるまでブロックする。
func (h *Host) Connect(remoteHost string, remotePort int) *Socket {
	h.mu.Lock()
	localPort := h.nextPort
	h.nextPort++

	s := &Socket{
		host:       h,
		localPort:  localPort,
		remoteHost: remoteHost,
		remotePort: remotePort,
		tcb: &TCB{
			state:       StateSynSent,
			sndNxt:      nextISN(), // 自分の初期シーケンス番号(ISN)
			recvCh:      make(chan []byte, 128),
			establishCh: make(chan struct{}),
		},
	}
	key := connKey{localPort, remoteHost, remotePort}
	h.conns[key] = s

	// ① SYN を送る。seq=自分のISN。
	syn := Packet{
		SrcHost: h.addr, DstHost: remoteHost,
		SrcPort: localPort, DstPort: remotePort,
		SYN: true, Seq: s.tcb.sndNxt,
	}
	s.tcb.sndNxt++ // SYNはseqを1消費する(データ1バイト分のように扱う)
	h.trace("Connect: CLOSED -> SYN_SENT : send %s", syn.flagsString()+funcSeq(syn))
	h.mu.Unlock()

	h.net.send(syn)

	<-s.tcb.establishCh // SYN+ACKを受けてACKを返し、確立するまで待つ
	return s
}

// ============================================================
//   受信パケットの処理 = TCPの状態遷移エンジン
// ============================================================

func (h *Host) handle(p Packet) {
	h.mu.Lock()
	defer h.mu.Unlock()

	key := connKey{localPort: p.DstPort, remoteHost: p.SrcHost, remotePort: p.SrcPort}

	// すでに存在するコネクション宛てか?
	if s, ok := h.conns[key]; ok {
		h.handleConn(s, p)
		return
	}

	// 新規のSYN(接続要求)で、待ち受けソケットがあるか?
	if p.SYN && !p.ACK {
		ln, ok := h.listeners[p.DstPort]
		if !ok {
			// 窓口が無い -> 本物のOSはRST(接続拒否)を返す。ここでは破棄。
			h.trace("recv %s だが :%d で誰もListenしていない -> 破棄", p.flagsString(), p.DstPort)
			return
		}
		h.acceptSYN(ln, p, key)
		return
	}

	h.trace("recv %s (該当コネクション無し) -> 破棄", p.flagsString())
}

// acceptSYN: 待ち受けソケットがSYNを受けたとき。
// 新しい通信ソケット(コネクション)を SYN_RCVD 状態で作り、SYN+ACKを返す。
func (h *Host) acceptSYN(ln *Socket, p Packet, key connKey) {
	conn := &Socket{
		host:       h,
		localPort:  p.DstPort,
		remoteHost: p.SrcHost,
		remotePort: p.SrcPort,
		tcb: &TCB{
			state:       StateSynReceived,
			sndNxt:      nextISN(),  // サーバ側の初期シーケンス番号
			rcvNxt:      p.Seq + 1,  // 相手のSYN(seq)の次を期待
			recvCh:      make(chan []byte, 128),
			establishCh: make(chan struct{}, 1),
		},
	}
	h.conns[key] = conn

	// ② SYN+ACK を返す。seq=自分のISN, ack=相手のseq+1。
	synack := Packet{
		SrcHost: h.addr, DstHost: p.SrcHost,
		SrcPort: p.DstPort, DstPort: p.SrcPort,
		SYN: true, ACK: true,
		Seq: conn.tcb.sndNxt, Ack: conn.tcb.rcvNxt,
	}
	conn.tcb.sndNxt++ // SYNはseqを1消費
	h.trace("recv %s seq=%d : LISTEN -> SYN_RCVD : send [SYN,ACK] seq=%d ack=%d",
		p.flagsString(), p.Seq, synack.Seq, synack.Ack)
	h.net.send(synack)
}

// handleConn: 既存コネクション宛てのパケットを、現在の状態に応じて処理する。
// これがTCPの状態遷移そのもの。
func (h *Host) handleConn(s *Socket, p Packet) {
	switch s.tcb.state {

	case StateSynSent:
		// クライアントが SYN+ACK を受け取った。
		if p.SYN && p.ACK {
			s.tcb.rcvNxt = p.Seq + 1
			// ③ 最後のACKを返して確立。
			ack := Packet{
				SrcHost: h.addr, DstHost: s.remoteHost,
				SrcPort: s.localPort, DstPort: s.remotePort,
				ACK: true, Seq: s.tcb.sndNxt, Ack: s.tcb.rcvNxt,
			}
			s.tcb.state = StateEstablished
			h.trace("recv [SYN,ACK] seq=%d ack=%d : SYN_SENT -> ESTABLISHED : send [ACK] ack=%d",
				p.Seq, p.Ack, ack.Ack)
			h.net.send(ack)
			close(s.tcb.establishCh) // Connect()のブロックを解除
		}

	case StateSynReceived:
		// サーバが最後のACKを受け取った -> 確立。accept待ちキューへ。
		if p.ACK && !p.SYN {
			s.tcb.state = StateEstablished
			h.trace("recv [ACK] ack=%d : SYN_RCVD -> ESTABLISHED : accept待ちキューへ投入", p.Ack)
			if ln, ok := h.listeners[s.localPort]; ok {
				ln.tcb.acceptCh <- s // ここで初めて Accept() が返れる
			}
		}

	case StateEstablished:
		// データ到着
		if len(p.Payload) > 0 {
			s.tcb.rcvNxt = p.Seq + uint32(len(p.Payload))
			// 受領確認(ACK)を返す
			ack := Packet{
				SrcHost: h.addr, DstHost: s.remoteHost,
				SrcPort: s.localPort, DstPort: s.remotePort,
				ACK: true, Seq: s.tcb.sndNxt, Ack: s.tcb.rcvNxt,
			}
			h.trace("recv data=%q seq=%d : ESTABLISHED : send [ACK] ack=%d",
				string(p.Payload), p.Seq, ack.Ack)
			h.net.send(ack)
			s.tcb.recvCh <- p.Payload // アプリのRecv()へ届ける
		}
		// FIN(切断要求)
		if p.FIN {
			s.tcb.rcvNxt = p.Seq + 1
			h.trace("recv [FIN] : 相手が切断 -> recvチャネルを閉じる(EOF)")
			ack := Packet{
				SrcHost: h.addr, DstHost: s.remoteHost,
				SrcPort: s.localPort, DstPort: s.remotePort,
				ACK: true, Seq: s.tcb.sndNxt, Ack: s.tcb.rcvNxt,
			}
			h.net.send(ack)
			close(s.tcb.recvCh) // Recv()がEOF(ok=false)になる
		}
		// データに対する純粋なACK(自分が送ったデータの受領確認)は、
		// このミニ実装では特に何もしない(再送制御を省略しているため)。
	}
}

// appSend: アプリが Send() したデータをパケットにして送る。
func (h *Host) appSend(s *Socket, data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	pkt := Packet{
		SrcHost: h.addr, DstHost: s.remoteHost,
		SrcPort: s.localPort, DstPort: s.remotePort,
		ACK: true, PSH: true,
		Seq: s.tcb.sndNxt, Ack: s.tcb.rcvNxt,
		Payload: data,
	}
	s.tcb.sndNxt += uint32(len(data)) // 送ったバイト数だけseqを進める
	h.trace("appSend data=%q : send [PSH,ACK] seq=%d len=%d", string(data), pkt.Seq, len(data))
	h.net.send(pkt)
}

// appClose: アプリが Close() したとき。簡易版としてFINを送るだけ。
func (h *Host) appClose(s *Socket) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fin := Packet{
		SrcHost: h.addr, DstHost: s.remoteHost,
		SrcPort: s.localPort, DstPort: s.remotePort,
		FIN: true, ACK: true,
		Seq: s.tcb.sndNxt, Ack: s.tcb.rcvNxt,
	}
	s.tcb.sndNxt++
	s.tcb.state = StateClosing
	h.trace("Close: send [FIN] seq=%d", fin.Seq)
	h.net.send(fin)
}

// funcSeq はログ整形の小さなヘルパ(SYNパケットの seq を文字列に)。
func funcSeq(p Packet) string {
	return " seq=" + itoa(int(p.Seq))
}

// itoa: strconvに頼らない小さな整数→文字列(01-echo/rawと同じ発想)。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
