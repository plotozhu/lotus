package transp2p

/***

 */
import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/bluele/gcache"
	lru "github.com/bluele/gcache"
	"github.com/smallnest/rpcx/log"

	util "github.com/filecoin-project/lotus/chain/pnyx/rtutil"
	"github.com/filecoin-project/lotus/chain/pnyx/transpp"
	"github.com/fxamacker/cbor"

	"github.com/libp2p/go-libp2p-core/peer"
	peerstore "github.com/libp2p/go-libp2p-peerstore"
)

//RouteIntf 路由管理器接口
type RouteIntf interface {
	GetRoutes(dest peer.ID) ([]*RouteTableItem, error)
	GetBestRoute(dest peer.ID) (*RouteTableItem, error)
	UpdateRoute(dest, next peer.ID, ttl uint8)
	UpdateNeighbour(next peer.ID, breakdown bool)
}

//ConnectionNotifier 连接和断开的知
type ConnectionNotifier interface {
	GetNeighbours() []peer.ID
	OnConnected(func(peerID peer.ID))
	OnDisconnected(func(peerID peer.ID))
}

const (
	maxPendDataCnt          = 1024
	maxFindCache            = 1024
	handleRoute             = "R/CMD"
	handlePingPong          = "R/PING"
	handleData              = "R/DATA"
	cmdFind           uint8 = 0x01
	cmdFindResp       uint8 = 0x02
	cmdSendData       uint8 = 0x10
	cmdDataResp       uint8 = 0x11
	cmdPing           uint8 = 0x01
	cmdPong           uint8 = 0x02
	maxSendingPacket        = 64
	maxResponsedCnt         = 1024 * 100
	maxNeighbours           = 128
	pingInterval            = 20 * time.Second
	statePinged             = 1
	statePonged             = 2
	statePingReceived       = 3
)

type PingData struct {
	Type uint8
}
type PingPongState struct {
	timer *time.Timer
	state int
}
type PendingData struct {
	Dst    peer.ID
	Alpha  uint8
	OrgTTL uint8
	Data   []byte
	Wait   chan error
}
type DataHandle func(data []byte)

//TransP2P
type TransP2P struct {
	ppSvr          *transpp.TransPushPullService
	pendingCache   gcache.Cache //those waiting for routing
	self           peer.ID
	pstore         peerstore.Peerstore
	buckets        sync.Map
	dataChannel    chan *PendingData
	pingChannel    chan *PingData
	routeTab       RouteIntf
	currentFd      uint64
	lock           sync.Mutex
	waitingForResp sync.Map
	responsedCache gcache.Cache
	handle         DataHandle
	findRouteCahce gcache.Cache
	pingpongCache  gcache.Cache
	notifier       ConnectionNotifier

	ctx          context.Context
	ppCacheMutex sync.Mutex
}

// MsgFindRoute 是发现消息
type MsgFindRoute struct {
	Cmd      uint8
	Dst      peer.ID
	Src      peer.ID
	Alpha    uint8
	OrgTTL   uint8
	Ttl      uint8
	Outbound []byte
	Receipts []byte //used for token consume,current is nil
}

//MsgDataTrans msg for data transfer
type MsgDataTrans struct {
	Cmd      uint8
	Dst      peer.ID
	Src      peer.ID
	OrgTTL   uint8
	Ttl      uint8
	Fd       uint64
	Data     []byte
	Receipts []byte //used for token consume,current is nil
}

//CreateTransP2P create a P2P transfer service
func CreateTransP2P(ctx context.Context, self peer.ID, ppSvr *transpp.TransPushPullService, pstore peerstore.Peerstore, routeTab RouteIntf, notifier ConnectionNotifier, handle DataHandle) (*TransP2P, error) {
	var err error
	if routeTab == nil || reflect.ValueOf(routeTab).IsNil() {
		routeTab, err = CreateRouter()
		if err != nil {
			return nil, err
		}
	}
	transInst := TransP2P{
		ppSvr:          ppSvr,
		pendingCache:   lru.New(maxPendDataCnt).LRU().Build(), // 128, 10*time.Minute),
		self:           self,
		pstore:         pstore,
		routeTab:       routeTab,
		dataChannel:    make(chan *PendingData, maxSendingPacket),
		pingChannel:    make(chan *PingData, maxSendingPacket),
		responsedCache: lru.New(maxResponsedCnt).LRU().Build(), // 128, 10*time.Minute),
		findRouteCahce: lru.New(maxFindCache).LRU().Build(),    // 128, 10*time.Minute),
		currentFd:      0,
		handle:         handle,
		notifier:       notifier,
		ctx:            ctx,
	}
	transInst.pingpongCache = lru.New(maxNeighbours).LRU().Build()
	ppSvr.RegisterHandle(handlePingPong, transInst.procPingPong, nil)
	ppSvr.RegisterHandle(handleRoute, transInst.procRouteInfo, nil)
	ppSvr.RegisterHandle(handleData, transInst.procDataArrival, nil)
	go transInst.run(ctx)
	return &transInst, nil
}

func (tp *TransP2P) run(ctx context.Context) {
	tp.startPingService()
	for {
		select {
		case packet := <-tp.dataChannel:
			tp.createAndRelay(packet)
		case <-ctx.Done():
			return
		}
	}
}
func (tp *TransP2P) resetPingWorker(ctx context.Context, peerID peer.ID, state int) {
	tp.ppCacheMutex.Lock()
	defer tp.ppCacheMutex.Unlock()
	pp, err := tp.pingpongCache.Get(peerID)
	var ppState *PingPongState
	var aTimer *time.Timer
	rand.Seed(time.Now().UnixNano())
	sendInterval := pingInterval + time.Duration(rand.Intn(5000))*time.Millisecond

	if err == nil {
		ppState = pp.(*PingPongState)
		ppState.timer.Reset(sendInterval)
		ppState.state = state
	} else {
		ppState = &PingPongState{
			state: state,
			timer: time.NewTimer(time.Minute),
		}
		ppState.timer.Reset(sendInterval)
		ppState.state = state
		tp.pingpongCache.Set(peerID, ppState)
		go func(sendInterval time.Duration, pp *PingPongState) {
			for {
				select {
				case <-aTimer.C:
					if ppState.state == statePinged {
						//ping but no pong response,remove it
						tp.routeTab.UpdateNeighbour(peerID, true)
						//DONOT RESTART timer and remove from cache, sync must be considered
						tp.ppCacheMutex.Lock()
						defer tp.ppCacheMutex.Unlock()
						tp.pingpongCache.Remove(peerID)
						return
					}
					tp.sendPingPongTo(peerID, cmdPing)
					tp.resetPingWorker(tp.ctx, peerID, statePinged)
					pp.timer.Reset(sendInterval)

				case <-ctx.Done():
					pp.timer.Stop()
					return
				}

			}
		}(sendInterval, ppState)
	}

}
func (tp *TransP2P) startPingService() {
	if tp.notifier != nil && !reflect.ValueOf(tp.notifier).IsNil() {

		for _, peerID := range tp.notifier.GetNeighbours() {
			tp.sendPingPongTo(peerID, cmdPing)
			tp.resetPingWorker(tp.ctx, peerID, statePinged)

		}
	}

}
func (tp *TransP2P) sendPingPongTo(peerID peer.ID, cmd uint8) {
	msg := PingData{
		Type: cmd,
	}
	pingMsg, err := cbor.Marshal(msg, cbor.CanonicalEncOptions())
	if err == nil {
		tp.ppSvr.SendToPeer(peerID, handlePingPong, pingMsg)

	} else {
		log.Infof("Marshal message failed:%v", msg)
	}
}

func (tp *TransP2P) procPingPong(lastHop peer.ID, data []byte, pInfo interface{}) error {
	var msg PingData
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}
	if msg.Type == cmdPing {
		tp.sendPingPongTo(lastHop, cmdPong)
		tp.resetPingWorker(tp.ctx, lastHop, statePingReceived)

	} else if msg.Type == cmdPong {
		tp.resetPingWorker(tp.ctx, lastHop, statePonged)
	} else {
		return fmt.Errorf("ping pong cmd not exist:%v", msg.Type)
	}

	tp.routeTab.UpdateNeighbour(lastHop, false)

	return nil
}

/**
 *  these two should be combined to one function
 */
func (tp *TransP2P) procRouteInfo(lastHop peer.ID, data []byte, pInfo interface{}) error {
	var msg MsgFindRoute
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}

	//update route table and process pending data if exists
	tp.routeTab.UpdateRoute(msg.Src, lastHop, msg.OrgTTL-msg.Ttl)
	go tp.procPending(msg.Src)
	switch msg.Cmd {
	case cmdFind:
		return tp.procFindReq(lastHop, &msg)
	case cmdFindResp:
		return tp.procFindResp(&msg)
	}
	return fmt.Errorf("cmd not exist:%v", msg.Cmd)
}

// when data is arrival, response when data is to myself, or relay it, token should be consumed in the future
func (tp *TransP2P) procDataArrival(lastHop peer.ID, data []byte, pInfo interface{}) error {
	var msg MsgDataTrans
	err := cbor.Unmarshal(data, msg)
	if err != nil {
		return err
	}
	//update route table and process pending data if exists
	tp.routeTab.UpdateRoute(msg.Src, lastHop, msg.OrgTTL-msg.Ttl)
	go tp.procPending(msg.Src)
	switch msg.Cmd {
	case cmdFind:
		return tp.procDataSend(lastHop, &msg)
	case cmdFindResp:
		return tp.procDataResp(&msg)
	}
	return fmt.Errorf("cmd not exist:%v", msg.Cmd)

}

func (tp *TransP2P) procFindReq(src peer.ID, msg *MsgFindRoute) error {

	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		//send response

		msg := MsgFindRoute{
			Cmd:    cmdFindResp,
			Dst:    msg.Src,
			Src:    tp.self,
			Alpha:  alpha,
			OrgTTL: msg.OrgTTL - msg.Ttl + 2,
			Ttl:    msg.OrgTTL - msg.Ttl + 2,
		}
		data, err := cbor.Marshal(msg, cbor.EncOptions{})
		if err != nil {
			return err
		}
		tp.ppSvr.SendToPeer(src, handleRoute, data)
		return nil
	}
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	return tp.sendRouteDataToNearer(msg)

}
func (tp *TransP2P) procFindResp(msg *MsgFindRoute) error {
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		//send response
		//inform and send data
		return nil
	}
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	return tp.sendRouteDataToNearer(msg)
}
func (tp *TransP2P) procDataSend(src peer.ID, msg *MsgDataTrans) error {
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		//send response
		responsed := fmt.Sprintf("%v:%v", msg.Src, msg.Fd)
		_, ok := tp.responsedCache.Get(responsed)
		if ok == nil {
			return nil
		}
		tp.responsedCache.SetWithExpire(responsed, true, expireTime)
		respMsg := &MsgDataTrans{Cmd: cmdDataResp, Dst: msg.Src, Src: tp.self, OrgTTL: msg.OrgTTL - msg.Ttl + 2, Ttl: msg.OrgTTL - msg.Ttl + 2, Fd: msg.Fd}
		if tp.handle != nil {
			go tp.handle(msg.Data)
		}

		data, err := cbor.Marshal(respMsg, cbor.EncOptions{})
		if err != nil {
			return err
		}

		tp.ppSvr.SendToPeer(src, handleData, data)
	}
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}
	//TODO calculation data and token
	return tp.relayData(msg, false)
}
func (tp *TransP2P) procDataResp(msg *MsgDataTrans) error {
	if strings.Compare(string(msg.Dst), string(tp.self)) == 0 {
		// finding who is wait for this
		result, ok := tp.waitingForResp.Load(msg.Fd)
		if ok {
			if !reflect.ValueOf(result).IsNil() {
				val := result.(chan error)
				if val != nil {
					go func() {
						val <- nil
					}()
				}
			}

		}
	}
	//if ttl > 0 relay
	if msg.Ttl > 0 {
		msg.Ttl--
	} else {
		return fmt.Errorf("ttl out")
	}

	//TODO calculation data and token,  and resign a receipts to msg.receipts
	return tp.relayData(msg, false)
}
func (tp *TransP2P) addPending(msg *MsgDataTrans) {
	tp.lock.Lock()
	defer tp.lock.Unlock()
	pendings, err := tp.pendingCache.Get(msg.Dst)
	if err == nil {
		result := pendings.([]*MsgDataTrans)
		tp.pendingCache.SetWithExpire(msg.Dst, append(result, msg), fastExpireTime)
	} else {
		tp.pendingCache.SetWithExpire(msg.Dst, append([]*MsgDataTrans{}, msg), fastExpireTime)
	}
}

func (tp *TransP2P) procPending(dst peer.ID) {
	tp.lock.Lock()
	defer tp.lock.Unlock()
	pendings, ok := tp.pendingCache.Get(dst)
	if ok == nil {
		result := pendings.([]*MsgDataTrans)
		for _, msg := range result {
			tp.relayData(msg, false)
		}
		tp.pendingCache.Remove(dst)
	}
}

/**
 *	relay data to next hop
 */
func (tp *TransP2P) relayData(msg *MsgDataTrans, autoFind bool) error {
	peers, err := tp.routeTab.GetRoutes(msg.Dst)
	if len(peers) == 0 || err != nil {
		//store data and to find next?
		//now simple discard message
		if autoFind {

		}

	} else {

		data, err := cbor.Marshal(msg, cbor.EncOptions{})
		if err != nil {
			for _, nextPeer := range peers {
				tp.ppSvr.SendToPeer(nextPeer.next, handleRoute, data)
			}
		}

	}
	return nil
}

//AddPeer TODO manage by route distance algorithm
func (tp *TransP2P) AddPeer(peerID peer.ID) {

}

//RemovePeer TODO manage peers by route distance algorithm
func (tp *TransP2P) RemovePeer(peer.ID, peer.ID) {

}

//DistInfo to store dist and peer pair
type DistInfo struct {
	dist uint64
	peer *peer.ID
}

//FindRoute Send to find a route command, ttl is the max-hop for find data and alpha is fan-out, outbound is optional and should be less than 64 bytes
// in the future, token should be provided
func (tp *TransP2P) FindRoute(targetPeer peer.ID, ttl uint8, alpha uint8, outbound []byte) error {

	if strings.Compare(string(targetPeer), string(tp.self)) == 0 {
		return nil
	}

	msg := MsgFindRoute{
		Cmd:      cmdFind,
		Dst:      targetPeer,
		Src:      tp.self,
		Alpha:    alpha,
		OrgTTL:   ttl,
		Ttl:      ttl,
		Outbound: outbound,
	}
	return tp.sendRouteDataToNearer(&msg)
}

func (tp *TransP2P) sendRouteDataToNearer(msg *MsgFindRoute) error {

	//Get \alpha nearer peerid and send data to
	//this is with very low effiecny and only used for evaluation ,a tree should be  used to find nodes as fast as possible
	distsmap := make(map[int][]*peer.ID)
	for _, peerID := range tp.pstore.Peers() {
		dist := util.Dist(peerID, msg.Dst)
		val, ok := distsmap[dist]
		if ok {
			distsmap[dist] = append(val, &peerID)
		} else {
			distsmap[dist] = append([]*peer.ID{}, &peerID)
		}
	}
	myDist := util.Dist(tp.self, msg.Dst)

	target := make([]*peer.ID, 0)
	for i := 63; i > myDist; i-- {
		val, ok := distsmap[i]
		if ok {
			target := append(target, val...)

			if len(target) >= int(alpha) {
				break
			}
		}
	}

	if len(target) > int(alpha) {
		target = target[0:alpha]
	}

	data, err := cbor.Marshal(msg, cbor.EncOptions{})
	if err != nil {
		return err
	}
	if len(target) == 0 {
		return nil
	}
	tp.ppSvr.SendToPeers(target, handleRoute, data)
	return nil
}

//SendData Send to find a route command, ttl is the max-hop for find data and alpha is fan-out, outbound is optional and should be less than 64 bytes
// in the future, token should be provided
func (tp *TransP2P) SendData(targetPeer peer.ID, ttl uint8, alpha uint8, data []byte, ret chan error) error {

	if strings.Compare(string(targetPeer), string(tp.self)) == 0 {
		return nil
	}

	pending := PendingData{Dst: targetPeer, Alpha: alpha, OrgTTL: ttl, Data: data, Wait: ret}
	go func() {
		tp.dataChannel <- &pending
	}()
	return nil
}

func (tp *TransP2P) createAndRelay(pending *PendingData) error {
	tp.currentFd++
	msg := &MsgDataTrans{
		Cmd:    cmdSendData,
		Dst:    pending.Dst,
		Src:    tp.self,
		OrgTTL: pending.OrgTTL,
		Ttl:    pending.OrgTTL,
		Fd:     tp.currentFd,
		Data:   pending.Data,
		//receipts []byte //used for token consume,current is nil
	}
	if pending.Wait != nil {
		tp.waitingForResp.Store(msg.Fd, pending.Wait)
	}
	return tp.relayData(msg, true)

}
