package main

import "log"

// Network は「配線(ケーブル/ルーター)」の役割。
// 本物では銅線や光ファイバ、無線だが、ここではただの配送係。
// パケットの宛先ホストを見て、そのホストの受信箱(inbox)に届けるだけ。
//
// ポイント: 配線はパケットの「中身」には一切興味がない。
//           SYNだろうがデータだろうが、宛先に届けるだけの存在。
type Network struct {
	hosts map[string]*Host // ホスト名(アドレス) -> ホスト
	trace bool             // パケットの流れをログ出力するか
}

func NewNetwork() *Network {
	return &Network{hosts: make(map[string]*Host)}
}

// NewHost はこの配線につながった新しいホスト(= 自作OS/カーネル1台)を作る。
func (n *Network) NewHost(addr string) *Host {
	h := &Host{
		addr:      addr,
		net:       n,
		inbox:     make(chan Packet, 1024), // 受信箱(NICの受信バッファ相当)
		listeners: make(map[int]*Socket),
		conns:     make(map[connKey]*Socket),
		nextPort:  50000, // クライアント側の一時ポート割り当て開始番号
	}
	n.hosts[addr] = h
	go h.run() // このホストのネットワークスタックを起動(受信ループ)
	return h
}

// send はパケットを宛先ホストの受信箱へ運ぶ。
// 配線をケーブルが1本通っているイメージ。順序は保たれる(channelなので)。
func (n *Network) send(p Packet) {
	if n.trace {
		log.Printf("   ~~ wire ~~> %s", p)
	}
	dst, ok := n.hosts[p.DstHost]
	if !ok {
		log.Printf("   !! 宛先ホスト %s が存在しない。パケット破棄", p.DstHost)
		return
	}
	// 受信箱に投函。buffered channel なので送り手はほぼブロックしない
	// (= 本物のNICが受信したパケットをバッファに積むのに相当)。
	dst.inbox <- p
}
