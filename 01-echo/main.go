// 01-echo/raw: net.Listen / ln.Accept を「自前実装」したEchoサーバー
//
// net パッケージを使わず、OSのシステムコール(syscall)を直接叩いて
// 待ち受けソケットの生成〜接続受付〜送受信までを書く。
// これを読むと、net.Listen / ln.Accept / conn.Read|Write の正体が
// 「socket() / bind() / listen() / accept() / read() / write() という
//
//	OSのシステムコールの薄いラッパー」だと分かる。
//
// 重要な気づき:
//
//	ソケットは Go のオブジェクトではなく、OSが管理する資源で、
//	アプリからは「ファイルディスクリプタ(fd)= ただの整数」として扱われる。
//	下のコードで lfd / cfd が全部 int なのがその証拠。
//
// 使い方:
//
//	go run ./01-echo/raw
//	別ターミナルで: nc localhost 8080
//
// 注意: macOS / Linux 用(syscallの定数がUnix系前提)。
package main

import (
	"log"
	"syscall"
)

func main() {
	// ===== net.Listen("tcp", ":8080") に相当する処理 =====
	lfd, err := myListen(8080)
	if err != nil {
		log.Fatalf("myListen 失敗: %v", err)
	}
	defer syscall.Close(lfd)
	log.Printf("待ち受けソケット(fd=%d)で :8080 を listen 中", lfd)

	for {
		// ===== ln.Accept() に相当する処理 =====
		cfd, sa, err := myAccept(lfd)
		if err != nil {
			log.Printf("myAccept 失敗: %v", err)
			continue
		}
		log.Printf("接続受付: 通信ソケット fd=%d (相手=%s)", cfd, sockaddrString(sa))

		// net版では go handleConn(conn) としていた部分。
		// ここでは簡単のため逐次処理(1接続ずつ)にしている。
		handleConn(cfd)
	}
}

// myListen は net.Listen("tcp", ...) が内部でやっていることを再現する。
// 戻り値の lfd が「待ち受けソケット」の実体 = ファイルディスクリプタ(整数)。
func myListen(port int) (int, error) {
	// ① socket(): 新しいソケットをOSに作ってもらう。
	//    AF_INET     = IPv4
	//    SOCK_STREAM = ストリーム型 = TCP
	//    戻り値 lfd はそのソケットを指すファイルディスクリプタ。
	lfd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return -1, err
	}

	// (おまけ) SO_REUSEADDR: サーバーを再起動したとき
	// "address already in use" になりにくくするための定番設定。
	if err := syscall.SetsockoptInt(lfd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(lfd)
		return -1, err
	}

	// ② bind(): このソケットに「自分の住所(IP:ポート)」を割り当てる。
	//    Addr = [0,0,0,0] は INADDR_ANY = 全インターフェースで待ち受ける意味。
	addr := &syscall.SockaddrInet4{
		Port: port,
		Addr: [4]byte{0, 0, 0, 0},
	}
	if err := syscall.Bind(lfd, addr); err != nil {
		syscall.Close(lfd)
		return -1, err
	}

	// ③ listen(): このソケットを「待ち受けソケット」に切り替える。
	//    第2引数 backlog = 受付待ち行列(まだAcceptしてない確立済み接続)の最大数。
	//    ここを境に、lfd は「接続要求を受け付ける窓口」専用になる。
	if err := syscall.Listen(lfd, 128); err != nil {
		syscall.Close(lfd)
		return -1, err
	}

	return lfd, nil
}

// myAccept は ln.Accept() が内部でやっていることを再現する。
// 「待ち受けソケット lfd」から、確立済み接続を1つ取り出して
// 「通信専用ソケット cfd」として返す。lfd と cfd が別物なのが要点。
func myAccept(lfd int) (int, syscall.Sockaddr, error) {
	// ④ accept(): 接続要求が来るまでブロックし、
	//    成立した接続のための【新しいソケット cfd】を返す。
	//    sa には相手(クライアント)のアドレスが入る。
	//    ※ 3ウェイハンドシェイク自体はOSが既に裏で完了させており、
	//      accept はその完成品を待ち行列から1個取り出すだけ。
	cfd, sa, err := syscall.Accept(lfd)
	if err != nil {
		return -1, nil, err
	}
	return cfd, sa, nil
}

// handleConn は conn.Read / conn.Write 相当を syscall.Read / syscall.Write で行う。
// net.Conn の正体も、結局この read()/write() システムコールのラッパー。
func handleConn(cfd int) {
	defer syscall.Close(cfd)

	buf := make([]byte, 1024)
	for {
		// ⑤ read(): 通信ソケットから届いているバイト列を読む。n = 読めたバイト数。
		n, err := syscall.Read(cfd, buf)
		if err != nil {
			log.Printf("read エラー (fd=%d): %v", cfd, err)
			return
		}
		// n == 0 は相手が接続を閉じた合図(EOF相当)。
		if n == 0 {
			log.Printf("切断: fd=%d", cfd)
			return
		}

		// ⑥ write(): 読んだぶん(buf[:n])をそのまま書き戻す = echo。
		if _, err := syscall.Write(cfd, buf[:n]); err != nil {
			log.Printf("write エラー (fd=%d): %v", cfd, err)
			return
		}
	}
}

// sockaddrString は SockaddrInet4 を "IP:ポート" の文字列にする(ログ表示用)。
func sockaddrString(sa syscall.Sockaddr) string {
	if a, ok := sa.(*syscall.SockaddrInet4); ok {
		ip := a.Addr
		return net4String(ip) + ":" + itoa(a.Port)
	}
	return "unknown"
}

func net4String(ip [4]byte) string {
	return itoa(int(ip[0])) + "." + itoa(int(ip[1])) + "." + itoa(int(ip[2])) + "." + itoa(int(ip[3]))
}

// 標準ライブラリの strconv を使わず、依存を最小にするための小さな整数→文字列変換。
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
