package net

import (
	"fmt"
	"github.com/nothollyhigh/kiss/log"
	"github.com/nothollyhigh/kiss/util"
	"io"
	"net"
	"sync"
	"time"
)

// tcp engine
type TcpEngin struct {
	sync.RWMutex

	// graceful
	sync.WaitGroup

	// connections
	clients map[*TcpClient]struct{}

	// proto handlers
	handlers map[uint32]func(*TcpClient, IMessage)
	// rpc method(string) handlers
	rpcMethodHandlers map[string]func(*RpcContext)

	// running flag
	running bool

	// codec
	Codec ICodec

	// tcp nodelay
	sockNoDelay bool
	// tcp client keepalive
	sockKeepAlive bool
	// tcp client send queue size
	sendQsize int
	// tcp client receive buf length
	sockRecvBufLen int
	// tcp client send buf length
	sockSendBufLen int
	// tcp client max packet length
	sockMaxPackLen int
	// tcp client linger seconds
	sockLingerSeconds int
	// tcp client keepalive interval
	sockKeepaliveTime time.Duration
	// tcp client receive block time
	sockRecvBlockTime time.Duration
	// tcp client send block time
	sockSendBlockTime time.Duration

	// new connection handler
	onNewConnHandler func(conn *net.TCPConn) error
	// create tcp client handler
	createClientHandler func(conn *net.TCPConn, parent *TcpEngin, cipher ICipher) *TcpClient
	// new tcp client handler
	onNewClientHandler func(client *TcpClient)
	// new cipher handler
	newCipherHandler func() ICipher
	// tcp client disconnected handler
	onDisconnectedHandler func(client *TcpClient)
	// tcp client send queue full handler
	sendQueueFullHandler func(*TcpClient, interface{})
	// tcp client receive handler
	recvHandler func(client *TcpClient) IMessage
	// tcp client send handler
	sendHandler func(client *TcpClient, data []byte) error
	// tcp client message handler
	onMsgHandler func(client *TcpClient, msg IMessage)
}

// on new connection
func (engine *TcpEngin) OnNewConn(conn *net.TCPConn) error {
	defer util.HandlePanic()

	if engine.onNewConnHandler != nil {
		return engine.onNewConnHandler(conn)
	}

	var err error
	if err = conn.SetNoDelay(engine.sockNoDelay); err != nil {
		log.Debug("SetNoDelay Error: %v.", err)
		goto ErrExit
	}

	if err = conn.SetKeepAlive(engine.sockKeepAlive); err != nil {
		log.Debug("SetKeepAlive Error: %v.", err)
		goto ErrExit
	}

	if engine.sockKeepAlive {
		if err = conn.SetKeepAlivePeriod(engine.sockKeepaliveTime); err != nil {
			log.Debug("SetKeepAlivePeriod Error: %v.", err)
			goto ErrExit
		}
	}

	if err = conn.SetReadBuffer(engine.sockRecvBufLen); err != nil {
		log.Debug("SetReadBuffer Error: %v.", err)
		goto ErrExit
	}
	if err = conn.SetWriteBuffer(engine.sockSendBufLen); err != nil {
		log.Debug("SetWriteBuffer Error: %v.", err)
		goto ErrExit
	}

	if err = conn.SetLinger(engine.sockLingerSeconds); err != nil {
		log.Debug("SetLinger Error: %v.", err)
		goto ErrExit
	}

	return nil

ErrExit:
	conn.Close()
	return err
}

// handle new connection
func (engine *TcpEngin) HandleNewConn(onNewConn func(conn *net.TCPConn) error) {
	engine.onNewConnHandler = onNewConn
}

// create client
func (engine *TcpEngin) CreateClient(conn *net.TCPConn, parent *TcpEngin, cipher ICipher) *TcpClient {
	if engine.createClientHandler != nil {
		return engine.createClientHandler(conn, parent, cipher)
	}
	return createTcpClient(conn, parent, cipher)
}

// setting create tcp client handler
func (engine *TcpEngin) HandleCreateClient(createClient func(conn *net.TCPConn, parent *TcpEngin, cipher ICipher) *TcpClient) {
	engine.createClientHandler = createClient
}

// on new client
func (engine *TcpEngin) OnNewClient(client *TcpClient) {
	if engine.onNewClientHandler != nil {
		engine.onNewClientHandler(client)
	}
}

// setting new client handler
func (engine *TcpEngin) HandleNewClient(onNewClient func(client *TcpClient)) {
	engine.onNewClientHandler = onNewClient
}

// new cipher
func (engine *TcpEngin) NewCipher() ICipher {
	if engine.newCipherHandler != nil {
		return engine.newCipherHandler()
	}
	return nil
}

// setting new cipher handler
func (engine *TcpEngin) HandleNewCipher(newCipher func() ICipher) {
	engine.newCipherHandler = newCipher
}

// on disconnected
func (engine *TcpEngin) OnDisconnected(client *TcpClient) {
	if engine.onDisconnectedHandler != nil {
		engine.onDisconnectedHandler(client)
	}
}

// setting disconnected handler
func (engine *TcpEngin) HandleDisconnected(onDisconnected func(client *TcpClient)) {
	pre := engine.onDisconnectedHandler
	engine.onDisconnectedHandler = func(c *TcpClient) {
		defer util.HandlePanic()
		if pre != nil {
			pre(c)
		}
		onDisconnected(c)
	}
}

// recv message
func (engine *TcpEngin) RecvMsg(client *TcpClient) IMessage {
	// defer util.HandlePanic()

	if engine.recvHandler != nil {
		return engine.recvHandler(client)
	}

	pkt := struct {
		err        error
		msg        *Message
		readLen    int
		dataLen    int
		remoteAddr string
	}{
		err: nil,
		msg: &Message{
			data: make([]byte, DEFAULT_MESSAGE_HEAD_LEN),
		},
		readLen:    0,
		dataLen:    0,
		remoteAddr: client.Conn.RemoteAddr().String(),
	}

	if pkt.err = client.Conn.SetReadDeadline(time.Now().Add(engine.sockRecvBlockTime)); pkt.err != nil {
		log.Debug("%s RecvMsg SetReadDeadline Err: %v.", client.Conn.RemoteAddr().String(), pkt.err)
		goto Exit
	}

	pkt.readLen, pkt.err = io.ReadFull(client.Conn, pkt.msg.data)
	if pkt.err != nil || pkt.readLen < DEFAULT_MESSAGE_HEAD_LEN {
		log.Debug("%s RecvMsg Read Head Err: %v, readLen: %d.", client.Conn.RemoteAddr().String(), pkt.err, pkt.readLen)
		goto Exit
	}

	pkt.dataLen = int(pkt.msg.BodyLen())

	if pkt.dataLen > 0 {
		if pkt.dataLen+DEFAULT_MESSAGE_HEAD_LEN > engine.sockMaxPackLen {
			log.Debug("%s RecvMsg Read Body Err: Msg Len(%d) > MAXPACK_LEN(%d)", client.Conn.RemoteAddr().String(), pkt.dataLen+DEFAULT_MESSAGE_HEAD_LEN, engine.sockMaxPackLen)
			goto Exit
		}

		if pkt.err = client.Conn.SetReadDeadline(time.Now().Add(engine.sockRecvBlockTime)); pkt.err != nil {
			log.Debug("%s RecvMsg SetReadDeadline Err: %v.", client.Conn.RemoteAddr().String(), pkt.err)
			goto Exit
		}

		pkt.msg.data = append(pkt.msg.data, make([]byte, pkt.dataLen)...)
		pkt.readLen, pkt.err = io.ReadFull(client.Conn, pkt.msg.data[DEFAULT_MESSAGE_HEAD_LEN:])
		if pkt.err != nil {
			log.Debug("%s RecvMsg Read Body Err: %v", client.Conn.RemoteAddr().String(), pkt.err)
			goto Exit
		}
	}

	pkt.msg.rawData = pkt.msg.data
	pkt.msg.data = nil
	if _, pkt.err = pkt.msg.Decrypt(client.RecvSeq(), client.RecvKey(), client.Cipher()); pkt.err != nil {
		log.Debug("%s RecvMsg Decrypt Err: %v", client.Conn.RemoteAddr().String(), pkt.err)
		goto Exit
	}

	return pkt.msg

Exit:
	return nil
}

// setting receive message handler
func (engine *TcpEngin) HandleRecv(recver func(client *TcpClient) IMessage) {
	engine.recvHandler = recver
}

// on tcp client send queue full
func (engine *TcpEngin) OnSendQueueFull(client *TcpClient, msg interface{}) {
	if engine.sendQueueFullHandler != nil {
		engine.sendQueueFullHandler(client, msg)
	}
}

// setting tcp client send queue full handler
func (engine *TcpEngin) HandleSendQueueFull(h func(*TcpClient, interface{})) {
	engine.sendQueueFullHandler = h
}

// tcp client send data
func (engine *TcpEngin) Send(client *TcpClient, data []byte) error {
	if engine.sendHandler != nil {
		return engine.sendHandler(client, data)
	}
	err := client.Conn.SetWriteDeadline(time.Now().Add(engine.sockSendBlockTime))
	if err != nil {
		log.Debug("%s Send SetReadDeadline Err: %v", client.Conn.RemoteAddr().String(), err)
		client.Stop()
		return err
	}

	nwrite, err := client.Conn.Write(data)
	if err != nil {
		log.Debug("%s Send Write Err: %v", client.Conn.RemoteAddr().String(), err)
		client.Stop()
		return err
	}
	if nwrite != len(data) {
		log.Debug("%s Send Write Half", client.Conn.RemoteAddr().String())
		client.Stop()
		return ErrTcpClientWriteHalf
	}
	return nil
}

// setting tcp client send data handler
func (engine *TcpEngin) HandleSend(sender func(client *TcpClient, data []byte) error) {
	engine.sendHandler = sender
}

func (engine *TcpEngin) OnMessage(client *TcpClient, msg IMessage) {
	if !engine.running {
		// switch msg.Cmd() {
		// case CmdPing:
		// case CmdSetReaIp:
		// case CmdRpcMethod:
		// case CmdRpcError:
		// default:
		// 	log.Debug("engine is not running, ignore cmd %X, ip: %v", msg.Cmd(), client.Ip())
		// 	return
		// }
		return
	}

	if engine.onMsgHandler != nil {
		engine.onMsgHandler(client, msg)
		return
	}

	cmd := msg.Cmd()
	if cmd == CmdPing {
		client.SendMsg(ping2Msg)
		return
	}
	if cmd == CmdPing2 {
		return
	}

	if handler, ok := engine.handlers[cmd]; ok {
		engine.Add(1)
		defer engine.Done()
		defer util.HandlePanic()
		handler(client, msg)
	} else {
		log.Debug("no handler for cmd %v", cmd)
	}
}

// setting message handler
func (engine *TcpEngin) HandleMessage(onMsg func(client *TcpClient, msg IMessage)) {
	engine.onMsgHandler = onMsg
}

// handle message by cmd
func (engine *TcpEngin) Handle(cmd uint32, handler func(client *TcpClient, msg IMessage)) {
	if cmd == CmdPing {
		log.Panic(ErrorReservedCmdPing.Error())
	}
	if cmd == CmdSetReaIp {
		log.Panic(ErrorReservedCmdSetRealip.Error())
	}
	if cmd == CmdRpcMethod {
		log.Panic(ErrorReservedCmdRpcMethod.Error())
	}
	if cmd == CmdRpcError {
		log.Panic(ErrorReservedCmdRpcError.Error())
	}
	if cmd > CmdUserMax {
		log.Panic(ErrorReservedCmdInternal.Error())
	}
	if _, ok := engine.handlers[cmd]; ok {
		log.Panic("Handle failed: handler for cmd %v exists", cmd)
	}
	engine.handlers[cmd] = handler
}

// handle rpc cmd
func (engine *TcpEngin) HandleRpcCmd(cmd uint32, handler func(ctx *RpcContext), async bool) {
	if cmd == CmdPing {
		panic(ErrorReservedCmdPing)
	}
	if cmd == CmdSetReaIp {
		panic(ErrorReservedCmdSetRealip)
	}
	if cmd == CmdRpcMethod {
		panic(ErrorReservedCmdRpcMethod)
	}
	if cmd == CmdRpcError {
		panic(ErrorReservedCmdRpcError)
	}
	if cmd > CmdUserMax {
		panic(ErrorReservedCmdInternal)
	}
	if _, ok := engine.handlers[cmd]; ok {
		panic(fmt.Errorf("HandleRpcCmd failed: handler for cmd %v exists", cmd))
	}
	if async {
		engine.handlers[cmd] = func(client *TcpClient, msg IMessage) {
			util.Go(func() {
				handler(&RpcContext{client: client, message: msg})
			})
		}
	} else {
		engine.handlers[cmd] = func(client *TcpClient, msg IMessage) {
			handler(&RpcContext{client: client, message: msg})
		}
	}
}

// on rpc method call
func (engine *TcpEngin) onRpcMethod(client *TcpClient, imsg IMessage) {
	msg := imsg.(*Message)
	data := msg.Body()
	if len(data) < 2 {
		client.SendMsg(NewRpcMessage(CmdRpcError, msg.RpcSeq(), []byte("invalid rpc payload")))
		return
	}
	methodLen := int(data[len(data)-1])
	if methodLen <= 0 || methodLen >= 128 || len(data)-1 < methodLen {
		client.SendMsg(NewRpcMessage(CmdRpcError, msg.RpcSeq(), []byte(fmt.Sprintf("invalid rpc method length %d, should be (1-127)", methodLen))))
		return
	}
	method := string(data[(len(data) - 1 - methodLen):(len(data) - 1)])
	handler, ok := engine.rpcMethodHandlers[method]
	if !ok {
		client.SendMsg(NewRpcMessage(CmdRpcError, msg.RpcSeq(), []byte(fmt.Sprintf("invalid rpc method %s", method))))
		return
	}
	// rawmsg := msg.(IMessage)
	msg.data = msg.data[:(len(msg.data) - 1 - methodLen)]
	handler(&RpcContext{method: method, client: client, message: msg})
}

// init rpc handler
func (engine *TcpEngin) initRpcHandler() {
	if engine.rpcMethodHandlers == nil {
		engine.rpcMethodHandlers = map[string]func(*RpcContext){}
		engine.handlers[CmdRpcMethod] = engine.onRpcMethod
	}
}

// setting handle rpc method
func (engine *TcpEngin) HandleRpcMethod(method string, handler func(ctx *RpcContext), args ...interface{}) {
	engine.initRpcHandler()
	if _, ok := engine.rpcMethodHandlers[method]; ok {
		panic(fmt.Errorf("HandleRpcMethod failed: handler for method %v exists", method))
	}

	async := false
	if len(args) > 0 {
		if a, ok := args[0].(bool); ok {
			async = a
		}
	}

	if async {
		engine.rpcMethodHandlers[method] = func(ctx *RpcContext) {
			util.Go(func() {
				handler(ctx)
			})
		}
	} else {
		engine.rpcMethodHandlers[method] = handler
	}
}

// socket nodelay
func (engine *TcpEngin) SockNoDelay() bool {
	return engine.sockNoDelay
}

// setting socket nodelay
func (engine *TcpEngin) SetSockNoDelay(nodelay bool) {
	engine.sockNoDelay = nodelay
}

// socket keepalive
func (engine *TcpEngin) SockKeepAlive() bool {
	return engine.sockKeepAlive
}

// setting socket keepalive
func (engine *TcpEngin) SetSockKeepAlive(keepalive bool) {
	engine.sockKeepAlive = keepalive
}

// socket keepalive interval
func (engine *TcpEngin) SockKeepaliveTime() time.Duration {
	return engine.sockKeepaliveTime
}

// setting socket keepalive interval
func (engine *TcpEngin) SetSockKeepaliveTime(keepaliveTime time.Duration) {
	engine.sockKeepaliveTime = keepaliveTime
}

// socket receive buf length
func (engine *TcpEngin) SockRecvBufLen() int {
	return engine.sockRecvBufLen
}

// setting socket receive buf length
func (engine *TcpEngin) SetSockRecvBufLen(recvBufLen int) {
	engine.sockRecvBufLen = recvBufLen
}

// socket send queue size
func (engine *TcpEngin) SendQueueSize() int {
	return engine.sendQsize
}

// setting socket send queue size
func (engine *TcpEngin) SetSendQueueSize(size int) {
	engine.sendQsize = size
}

// socket send buf length
func (engine *TcpEngin) SockSendBufLen() int {
	return engine.sockSendBufLen
}

// setting send receive buf length
func (engine *TcpEngin) SetSockSendBufLen(sendBufLen int) {
	engine.sockSendBufLen = sendBufLen
}

// socket receive block time
func (engine *TcpEngin) SockRecvBlockTime() time.Duration {
	return engine.sockRecvBlockTime
}

// setting socket receive block time
func (engine *TcpEngin) SetSockRecvBlockTime(recvBlockTime time.Duration) {
	engine.sockRecvBlockTime = recvBlockTime
}

// socket send block time
func (engine *TcpEngin) SockSendBlockTime() time.Duration {
	return engine.sockSendBlockTime
}

// setting socket send block time
func (engine *TcpEngin) SetSockSendBlockTime(sendBlockTime time.Duration) {
	engine.sockSendBlockTime = sendBlockTime
}

// socket max packet length
func (engine *TcpEngin) SockMaxPackLen() int {
	return engine.sockMaxPackLen
}

// setting socket max packet length
func (engine *TcpEngin) SetSockMaxPackLen(maxPackLen int) {
	engine.sockMaxPackLen = maxPackLen
}

// socket linger time
func (engine *TcpEngin) SockLingerSeconds() int {
	return engine.sockLingerSeconds
}

// setting socket linger time
func (engine *TcpEngin) SetSockLingerSeconds(sec int) {
	engine.sockLingerSeconds = sec
}

// broadcast
func (engine *TcpEngin) BroadCast(msg IMessage) {
	engine.Lock()
	defer engine.Unlock()
	for c, _ := range engine.clients {
		c.SendMsg(msg)
	}
}

// tcp engine factory
func NewTcpEngine() *TcpEngin {
	engine := &TcpEngin{
		clients:           map[*TcpClient]struct{}{},
		handlers:          map[uint32]func(*TcpClient, IMessage){},
		running:           true,
		Codec:             DefaultCodec,
		sockNoDelay:       DefaultSockNodelay,
		sockKeepAlive:     DefaultSockKeepalive,
		sendQsize:         DefaultSendQSize,
		sockRecvBufLen:    DefaultSockRecvBufLen,
		sockSendBufLen:    DefaultSockSendBufLen,
		sockMaxPackLen:    DefaultSockPackMaxLen,
		sockLingerSeconds: DefaultSockLingerSeconds,
		sockRecvBlockTime: DefaultSockRecvBlockTime,
		sockSendBlockTime: DefaultSockSendBlockTime,
		sockKeepaliveTime: DefaultSockKeepaliveTime,
	}

	cipher := NewCipherGzip(DefaultThreshold)
	engine.HandleNewCipher(func() ICipher {
		return cipher
	})

	return engine
}
